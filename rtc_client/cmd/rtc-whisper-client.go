package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	logr "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/log"
	whisper "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/backends/whisper.cpp"

	stt "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	"github.com/infinityp913/rtc-go-server/rtc_client"
	// stt "github.com/infinityp913/rtc-go-server/stt/engine"
)

const llmTime = time.Second * 2

var (
	logger = logr.New()
)

func main() {
	url := url.URL{Scheme: "wss", Host: "matherium.com", Path: "/go-server"}

	whisperCpp, err := whisper.New("../models/ggml-base.en.bin")
	if err != nil {
		logger.Fatal(err, "error creating whisper model")
	}

	transcriptionStream := make(chan stt.Document, 100)

	documentComposer := stt.NewDocumentComposer()
	documentComposer.FilterSegment(func(ts stt.TranscriptionSegment) bool {
		return ts.Text[0] == '.' || strings.ContainsAny(ts.Text, "[]()")
	})

	sttEngine, err := stt.New(stt.EngineParams{
		Transcriber:      whisperCpp,
		DocumentComposer: documentComposer,
		UseVad:           true,
	})

	rc, err := rtc_client.NewRiaClient(rtc_client.RiaConfig{
		Room:                "",
		Url:                 url,
		SttEngine:           sttEngine,
		TranscriptionStream: transcriptionStream,
	})
	if err != nil {
		logger.Fatal(err, "error creating saturday client")
	}

	promptBuilder := NewPromptBuilder(llmTime)

	onDocumentUpdate := func(document stt.Document) {
		transcriptionStream <- document
		promptBuilder.UpdatePrompt(document.NewText)
	}

	sttEngine.OnDocumentUpdate(onDocumentUpdate)

	go promptBuilder.Start(rc.Ae, rc.Rtc)
	defer promptBuilder.Stop()

	logger.Info("Starting Ria Client...")

	if err := rc.Start(); err != nil {
		logger.Fatal(err, "error starting Ria Client")
	}
}

// Struct to handle gathering STT output and passing to the Flask Server

type PromptBuilder struct {
	timer  *time.Timer // this tracks when to buffer and send to Flask
	prompt string      // this is where the end user's transcribed sentence/question is collected before sending to Flask
	cancel chan int    // channel to indicate exiting the infinite for loop in Start() function i.e., to stop sending data to Flask

	sync.Mutex // mutual exclusion lib to lock and unlock access to `prompt` by goroutines
}

// construct new PromptBuilder
func NewPromptBuilder(interval time.Duration) *PromptBuilder {
	return &PromptBuilder{
		timer:  time.NewTimer(interval),
		prompt: "",
		cancel: make(chan int),
	}
}

// update the prompt and reset the timer
func (p *PromptBuilder) UpdatePrompt(prompt string) {
	logger.Infof("UPDATING QnA PROMPT %s", prompt)
	p.Lock()
	defer p.Unlock()

	if p.prompt != "" {
		p.prompt += " "
	}

	p.prompt += prompt
	p.timer.Stop()
	p.timer.Reset(llmTime)
}

// Stop building prompts and sending to Flask server
func (p *PromptBuilder) Stop() {
	p.cancel <- 1
}

// Start building prompts and sending to Flask server
func (p *PromptBuilder) Start(ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) {
	for {
		// wait for the timer to fire OR Stop() to be called
		select {
		case <-p.timer.C: // indicates firing of timer aka the timer has counted down
			p.tryCallEngine(ae, rtc)
		case <-p.cancel: // indicates calling of Stop()
			logger.Info("shutting down llm interface")
			return
		}
	}
}

type FlaskResponse struct {
	// TODO: uncomment and use new_state
	// New_state string    `json:"new_state"`
	Pcm_arr []float32 `json:"response"`
}

var client = &http.Client{Timeout: 10 * time.Second}

func getJson(url string, jsonStrByte []byte, target interface{}) error {
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStrByte))
	if err != nil {
		log.Fatalln(err)
	}
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		logger.Info("Error at POST request!!")
		panic(err)
	}

	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(target)
}

func (p *PromptBuilder) tryCallEngine(ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) {
	p.Lock()

	// no prompt so wait again
	if p.prompt == "" {
		p.Unlock()
		return
	}

	currentPrompt := p.prompt
	p.prompt = ""

	p.Unlock()

	// *** Send currentPrompt to Flask server ***
	logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	url := "http://localhost:8000/get_response"       // Flask server running QnA NN + TTS NN is hosted here
	logger.Info("currentPrompt: ", currentPrompt)     // REMOVE AFTER DEBUG
	jsonStr := `{'end_user_input': '` + currentPrompt + `', 'curr_state':'4', 'client_id':'1', 'prompt_repeated_response':'0'}`
	// jsonStr := `{'text': ` + currentPrompt + `, 'curr_state':'4', 'client_id':'1', 'prompt_repeated_response':'0'}`
	var jsonStrByte = []byte(jsonStr)

	flaskResponse := new(FlaskResponse)
	getJson(url, jsonStrByte, flaskResponse)

	// extract pcm array from json
	var pcm_arr []float32 = flaskResponse.Pcm_arr
	logger.Info("len(pcm_arr): ", len(pcm_arr))

	// padding the audio with some silence -- seeing if this fixes the partial audio problem

	data := make([]float32, 38050)
	data = append(data, pcm_arr...)
	pcm_arr = data

	logger.Info("before encode") // REMOVE AFTER DEBUG

	ae.Encode(pcm_arr, 1, 22050)

	logger.Info("after encode") // REMOVE AFTER DEBUG

	// Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
	go rtc.ProcessOutgoingMedia()

	// *** End of sending currentPrompt to Flask server code ***
}
