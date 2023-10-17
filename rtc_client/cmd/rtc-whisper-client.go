package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	logr "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/log"
	// whisper "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/backends/whisper.cpp"
	whisper "github.com/infinityp913/rtc-go-server/stt/backends/whisper.cpp"

	// stt "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	"github.com/infinityp913/rtc-go-server/rtc_client"
	stt "github.com/infinityp913/rtc-go-server/stt/engine"
)

const llmTime = time.Millisecond * 1500
const NUM_STALL_MSGS = 3

var (
	logger = logr.New()
	msgs   = make([][]float32, NUM_STALL_MSGS)
)

func readStallMsgs() {
	stallMsgsFile, err := os.Open("stall_msgs_combined.txt")
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer stallMsgsFile.Close()

	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(stallMsgsFile)

	//the [...] instead of []: it ensures you get a (fixed size) array instead of a slice. So the values aren't fixed but the size is.
	var expectedMsgLengths = [...]int{35072, 27392, 29696}

	// Track the index of the stall message pcm array we're populating
	stallMsgIdx := -1

	// Track the index within the subarray
	subarrayIdx := 0

	// To store/track the "current" stall message pcm array

	for scanner.Scan() {
		line := scanner.Text()
		if line != "X" {
			floatValue, err := strconv.ParseFloat(line, 32)
			if err != nil {
				fmt.Println("Error parsing float:", err)
				return
			}
			msgs[stallMsgIdx][subarrayIdx] = float32(floatValue)
			subarrayIdx++
		} else {
			// increment stallMsgIdx since a new subarray is gonna get populated and we start reading the next message's pcm array
			stallMsgIdx++
			if stallMsgIdx < NUM_STALL_MSGS {
				// reset subArrayIdx to 0 since a new subarray is gonna get populated now
				subarrayIdx = 0
				msgs[stallMsgIdx] = make([]float32, expectedMsgLengths[stallMsgIdx])
			} else {
				break
			}
		}
	}
	logger.Info("", msgs[0][0])
	logger.Info("", msgs[1][2])
	logger.Info("", msgs[2][0])
	logger.Info("******************************* DONE ***************************")
}

func main() {
	// Load the stall messages like "One moment pls" into memory via the msgs array
	go readStallMsgs()

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

	// Sending signal to Browser to start the Browser client!
	logger.Info("Sending signal to RTCConn via a channel")
	// calling the following as a goroutine to enable sending the value (1) over the channel to rtc.SendHangupSignal(). Without a goroutine that has a sleep, the timing won't workout (inspiration: https://www.geeksforgeeks.org/select-statement-in-go-language/)
	go func() {
		// sleeping so that the value 1 is sent to the rtc.Hungup channel when control is blocked on the goroutine waiting for the value in the select-case block
		// Sleeping for 10ms
		time.Sleep(time.Millisecond * 10)
		rc.Rtc.StartBrowserClient <- 1 //this value serves as a signal to send data on the ria-hungup datachannel inside the rtc.SendHangupSignal() fn
	}()

	// this function creates the data channel and waits for the value(1) on the rtc.Hungup channel before sending the signal to the browser via the data channel
	rc.Rtc.SendStartBClientSignal()
	logger.Info("SENT SIGNAL TO START BROWSER CLIENT")
	// Done sending signal to start browser client

	init_state := riaSaysHello(rc.Ae, rc.Rtc)

	promptBuilder := NewPromptBuilder(llmTime, init_state) //2s timer starts here

	onDocumentUpdate := func(document stt.Document) {
		if document.NewText != "" {
			transcriptionStream <- document
			promptBuilder.UpdatePrompt(document.NewText, rc.Ae, rc.Rtc)
		}
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
	timer        *time.Timer // this tracks when to buffer and send to Flask
	prompt       string      // this is where the end user's transcribed sentence/question is collected before sending to Flask
	cancel       chan int    // channel to indicate exiting the infinite for loop in Start() function i.e., to stop sending data to Flask
	currentState int         // to store state for Ria's conversation

	sync.Mutex // mutual exclusion lib to lock and unlock access to `prompt` by goroutines
}

// construct new PromptBuilder
func NewPromptBuilder(interval time.Duration, init_state int) *PromptBuilder {
	logger.Info("TIMER HAS STARTED!") // REMOVE AFTER DEBUG
	return &PromptBuilder{
		timer:        time.NewTimer(interval), // Timer starts at this line
		prompt:       "",
		cancel:       make(chan int),
		currentState: init_state, // init_state is initialized by Ria's hello response's new_state
	}
}

// update the prompt and reset the timer
func (p *PromptBuilder) UpdatePrompt(prompt string, ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) {
	logger.Infof("UPDATING QnA PROMPT %s", prompt)
	p.Lock()
	defer p.Unlock()

	// if p.prompt == "" {
	// 	go sendStallMsg(ae, rtc)
	// }

	if p.prompt != "" {
		p.prompt += " "
	}

	p.prompt += prompt
	p.timer.Stop()
	p.timer.Reset(llmTime)
	logger.Infof("TIMER RESET!!!")
}

// Stop building prompts and sending to Flask server
func (p *PromptBuilder) Stop() {
	p.cancel <- 1
}

// Start building prompts and sending to Flask server
func (p *PromptBuilder) Start(ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) {
	for {
		logger.Infof("Inside Start()'s infinite loop")
		// wait for the timer to fire OR Stop() to be called
		select {
		case <-p.timer.C: // indicates firing of timer aka the 2s timer has counted down
			p.tryCallEngine(ae, rtc)
		case <-p.cancel: // indicates calling of Stop()
			logger.Info("shutting down llm interface")
			return
		}
	}
}

type FlaskResponse struct {
	// TODO: uncomment and use new_state
	New_state int       `json:"new_state"`
	Pcm_arr   []float32 `json:"response"`
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

func callkillGoClient(rtc *rtc_client.RTCConnection) func() {
	return func() {
		killGoClient(rtc)
	}
}

func killGoClient(rtc *rtc_client.RTCConnection) {
	logger.Info("CALLED killGoClient()!!")
	// calling the following as a goroutine to enable sending the value (1) over the channel to rtc.SendHangupSignal(). Without a goroutine that has a sleep, the timing won't workout (inspiration: https://www.geeksforgeeks.org/select-statement-in-go-language/)
	go func() {
		// sleeping so that the value 1 is sent to the rtc.Hungup channel when control is blocked on the goroutine waiting for the value in the select-case block
		// 100ms was chosen randomly to be short but enough time (this only adds to the time after Ria stops speakign before the UI i supdated to reflect hanign up)
		time.Sleep(time.Millisecond * 100)
		rtc.Hungup <- 1 //this value serves as a signal to send data on the ria-hungup datachannel inside the rtc.SendHangupSignal() fn
	}()

	// this function creates the data channel and waits for the value(1) on the rtc.Hungup channel before sending the signal to the browser via the data channel
	rtc.SendHangupSignal()
	logger.Info("SENT SIGNAL TO BROWSER")

	// sleeping for 500ms before exiting so that the above logic runs before killing the whole go client
	time.Sleep(time.Millisecond * 500)
	os.Exit(1)
}

// This function sends the current prompt (i.e., current message from the end user) to Flask
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

	url := "http://localhost:8000/get_response" // Flask server running QnA NN + TTS NN is hosted here

	logger.Info("The current_prompt being sent to Flask: ", currentPrompt)
	p.Lock() // locking since we're going to access p.currentState
	var jsonStrByte = []byte(`{"end_user_input": "` + currentPrompt + `", "curr_state":"` + strconv.Itoa(p.currentState) + `", "client_id":"1", "prompt_repeated_response":"0"}`)
	flaskResponse := new(FlaskResponse)

	logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	getJson(url, jsonStrByte, flaskResponse)
	logger.Info("Pcm Array Response Received")

	// extract pcm array from json
	var pcm_arr []float32 = flaskResponse.Pcm_arr

	p.currentState = flaskResponse.New_state
	p.Unlock()

	// padding the audio with some silence

	data := make([]float32, 38050)
	data = append(data, pcm_arr...)
	pcm_arr = data

	logger.Info("before encode") // REMOVE AFTER DEBUG

	ae.Encode(pcm_arr, 1, 22050)

	logger.Info("after encode") // REMOVE AFTER DEBUG

	// Logger.Info("calling go rtc.processOutgoingMedia") // REMOVE AFTER DEBUG
	go rtc.ProcessOutgoingMedia()

	// *** End of sending currentPrompt to Flask server code ***

	// If the state sent back by the Flask server is 4 then end the inference after 15s
	if flaskResponse.New_state == 4 {
		f := callkillGoClient(rtc)
		time.AfterFunc(15*time.Second, f)
	}

}

func riaSaysHello(ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) int {
	logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	// send POST req to the URL with user_input and get the json containing pcm
	url := "http://localhost:8000/get_response"

	// Sending curr_state 0 signal to flask along with a hard-coded hello (content of endu_user_input doesn't matter)
	// This is to get the intro as response
	var jsonStrByte = []byte(`{"end_user_input":"Hello!", "curr_state":"0", "client_id":"1", "prompt_repeated_response":"0"}`)

	flaskResponse := new(FlaskResponse)
	getJson(url, jsonStrByte, flaskResponse)

	// extract pcm array from json
	var pcm_arr []float32 = flaskResponse.Pcm_arr
	new_state := flaskResponse.New_state
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
	return new_state
}

func sendStallMsg(ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) {
	randomIndex := rand.Intn(len(msgs))
	chosen_msg := msgs[randomIndex]

	// padding the audio with some silence -- seeing if this fixes the partial audio problem

	data := make([]float32, 38050)
	data = append(data, chosen_msg...)
	chosen_msg = data

	ae.Encode(chosen_msg, 1, 22050)

	// Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
	go rtc.ProcessOutgoingMedia()
}
