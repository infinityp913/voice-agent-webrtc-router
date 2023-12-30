package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
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
	var expectedMsgLengths = [...]int{25088, 35072, 27392}

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

	// // this function creates the data channel and waits for the value(1) on the rtc.Hungup channel before sending the signal to the browser via the data channel
	// rc.Rtc.SendStartBClientSignal()
	// logger.Info("SENT SIGNAL TO START BROWSER CLIENT")
	// Done sending signal to start browser client

	if err := rc.CreateOfferAndSetLocalDescription(); err != nil {
		logger.Fatal(err, "error creating offer")
	} //NOV 28

	// time.Sleep(400 * time.Millisecond)

	init_state := riaSaysHello(rc.Ae, rc.Rtc)

	// commented nov 29
	// f := callRiaSaysHello(rc)
	// time.AfterFunc(10000*time.Millisecond, f) // this is to ensure that the browser client has answered the offer before calling riaSaysHello()
	// time.Sleep(10 * time.Second)              // nov 29

	pauseFunc := func() {
		rc.PauseRia()
	}

	unpauseFunc := func() {
		rc.UnpauseRia()
	}

	promptBuilder := NewPromptBuilder(llmTime, init_state, pauseFunc, unpauseFunc) //2s timer starts here

	onDocumentUpdate := func(document stt.Document) {
		if document.NewText == "" {
			logger.Info("Empty text!!!!!!!!!!!!!!!!!!!!!!")
		} else {
			transcriptionStream <- document
			promptBuilder.UpdatePrompt(document.NewText, rc.Ae, rc.Rtc)
		}
	}

	sttEngine.OnDocumentUpdate(onDocumentUpdate)

	go promptBuilder.Start(rc.Ae, rc.Rtc)
	defer promptBuilder.Stop()

	logger.Info("Starting Ria Client...")

	// COMMENTED NOV 28
	// if err := rc.Start(); err != nil {
	// 	logger.Fatal(err, "error starting Ria Client")
	// }

	// nov 27 -- for media reception
	rc.Ae.Start()
	rc.WaitForDone() // nov 27
}

// nov 29
func callRiaSaysHello(rc *rtc_client.RiaClient) func() {
	return func() {
		riaSaysHello(rc.Ae, rc.Rtc)
	}
}

// Struct to handle gathering STT output and passing to the Flask Server

type PromptBuilder struct {
	timer        *time.Timer // this tracks when to buffer and send to Flask
	prompt       string      // this is where the end user's transcribed sentence/question is collected before sending to Flask
	cancel       chan int    // channel to indicate exiting the infinite for loop in Start() function i.e., to stop sending data to Flask
	currentState int         // to store state for Ria's conversation

	sync.Mutex // mutual exclusion lib to lock and unlock access to `prompt` by goroutines

	// callback to pause Ria's listening i.e., stop processing RTP packets
	pauseFunc func()
	// callback to unpause Ria's listening i.e., stop processing RTP packets
	unpauseFunc func()
}

// construct new PromptBuilder
func NewPromptBuilder(interval time.Duration, init_state int, pauseFunc func(), unpauseFunc func()) *PromptBuilder {
	logger.Info("TIMER HAS STARTED!") // REMOVE AFTER DEBUG
	return &PromptBuilder{
		timer:        time.NewTimer(interval), // Timer starts at this line
		prompt:       "",
		cancel:       make(chan int),
		currentState: init_state,  // init_state is initialized by Ria's hello response's new_state
		pauseFunc:    pauseFunc,   // to pause Ria listening to the end user i.e., stop processing RTP packets
		unpauseFunc:  unpauseFunc, // to unpause Ria listening to the end user i.e., resume processing RTP packets
	}
}

// update the prompt and reset the timer
func (p *PromptBuilder) UpdatePrompt(prompt string, ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) {
	logger.Infof("UPDATING QnA PROMPT %s", prompt)
	p.Lock()
	defer p.Unlock()

	// // p.prompt being empty indicates that it's the start of a new question/speech
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
			// p.Lock()
			p.tryCallEngine(ae, rtc)
			// p.Unlock()
		case <-p.cancel: // indicates calling of Stop()
			logger.Info("shutting down llm interface")
			return
		}
	}
}

type FlaskResponse struct {
	// TODO: uncomment and use new_state
	New_state int    `json:"new_state"`
	Wav_arr   string `json:"response"`
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

type FlaskResponsePcm struct {
	// TODO: uncomment and use new_state
	Audio    string `json:"audio"`
	NewState string `json:"new_state"`
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

// type WavFrame struct {
// 	Index int
// 	Data  []byte
// }

// chunkWav will split the provided wav audio into properly sized frames
// func ChunkWav(wav []byte, sampleRate int) []rtc_client.WavFrame {
// 	// the amount of samples that fit into a frame
// 	outputFrameSize := 1 * 20 * 22050 / 1000
// 	// TODO make sure this rounds up
// 	totalFrames := len(wav) / outputFrameSize

// 	frames := make([]rtc_client.WavFrame, 0, totalFrames)

// 	idx := 0
// 	for idx <= totalFrames {
// 		wavLen := len(wav)
// 		// we have at least a full frame left
// 		if wavLen > outputFrameSize {
// 			logger.Debug("Got a full frame")
// 			frames = append(frames, rtc_client.WavFrame{Index: idx, Data: wav[:outputFrameSize]})
// 			// chop frame off of input
// 			wav = wav[outputFrameSize:]
// 			idx++
// 		} else {
// 			// we have less than a full frame so lets pad with silence
// 			sampleDelta := outputFrameSize - wavLen
// 			silence := make([]byte, sampleDelta)

// 			logger.Debugf("Got a partial frame len %d padding with %d silence samples", wavLen, len(silence))

// 			frames = append(frames, rtc_client.WavFrame{Index: idx, Data: append(wav, silence...)})
// 			break
// 		}
// 	}

// 	logger.Debugf("got %d frames", len(frames))

// 	return frames
// }

// chunkOpus will split the provided opus audio into properly sized frames
func ChunkOpus(opus []byte, sampleRate int) []rtc_client.OpusFrame {
	// the amount of samples that fit into a frame
	outputFrameSize := 1 * 20 * 22050 / 1000
	// TODO make sure this rounds up
	totalFrames := len(opus) / outputFrameSize

	frames := make([]rtc_client.OpusFrame, 0, totalFrames)

	idx := 0
	for idx <= totalFrames {
		opusLen := len(opus)
		// we have at least a full frame left
		if opusLen > outputFrameSize {
			logger.Debug("Got a full frame")
			frames = append(frames, rtc_client.OpusFrame{Index: idx, Data: opus[:outputFrameSize]})
			// chop frame off of input
			opus = opus[outputFrameSize:]
			idx++
		} else {
			// we have less than a full frame so lets pad with silence
			sampleDelta := outputFrameSize - opusLen
			silence := make([]byte, sampleDelta)

			logger.Debugf("Got a partial frame len %d padding with %d silence samples", opusLen, len(silence))

			frames = append(frames, rtc_client.OpusFrame{Index: idx, Data: append(opus, silence...)})
			break
		}
	}

	logger.Debugf("got %d frames", len(frames))

	return frames
}

// chunkPcm will split the provided opus audio into properly sized frames
func ChunkPcm(pcm []byte, sampleRate int, frameSizeMs int) []rtc_client.PcmFrame {
	// the amount of samples that fit into a frame
	outputFrameSize := 1 * frameSizeMs * sampleRate / 1000
	// TODO make sure this rounds up
	totalFrames := len(pcm) / outputFrameSize

	frames := make([]rtc_client.PcmFrame, 0, totalFrames)

	idx := 0
	for idx <= totalFrames {
		pcmLen := len(pcm)
		// we have at least a full frame left
		if pcmLen > outputFrameSize {
			logger.Debug("Got a full frame")
			frames = append(frames, rtc_client.PcmFrame{Index: idx, Data: pcm[:outputFrameSize]})
			// chop frame off of input
			pcm = pcm[outputFrameSize:]
			idx++
		} else {
			// we have less than a full frame so lets pad with silence
			sampleDelta := outputFrameSize - pcmLen
			silence := make([]byte, sampleDelta)

			logger.Debugf("Got a partial frame len %d padding with %d silence samples", pcmLen, len(silence))

			frames = append(frames, rtc_client.PcmFrame{Index: idx, Data: append(pcm, silence...)})
			break
		}
	}

	logger.Debugf("got %d frames", len(frames))

	return frames
}

// This function sends the current prompt (i.e., current message from the end user) to Flask
func (p *PromptBuilder) tryCallEngine(ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) {
	// p.Lock()

	// // no prompt so wait again
	// if p.prompt == "" {
	// 	p.Unlock()
	// 	return
	// }

	// currentPrompt := p.prompt
	// p.prompt = ""

	// p.Unlock()

	// // pause Ria  listening so we dont interrupt the response streaming
	// p.pauseFunc()

	// // *** Send currentPrompt to Flask server ***

	// url := "http://localhost:8000/get_response" // Flask server running QnA NN + TTS NN is hosted here

	// logger.Info("The current_prompt being sent to Flask: ", currentPrompt)
	// p.Lock() // locking since we're going to access p.currentState
	// var jsonStrByte = []byte(`{"end_user_input": "` + currentPrompt + `", "curr_state":"` + strconv.Itoa(p.currentState) + `", "client_id":"1", "prompt_repeated_response":"0"}`)
	// flaskResponse := new(FlaskResponse)

	// logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	// getJson(url, jsonStrByte, flaskResponse)
	// logger.Info("Pcm Array Response Received")

	// // extract pcm array from json
	// var wav_arr []byte = []byte(flaskResponse.Wav_arr)
	// logger.Info("len(wav_arr): ", len(wav_arr))

	// p.currentState = flaskResponse.New_state
	// p.Unlock()

	// // // padding the audio with some silence -- this is important, without this the start of the audio gets cut off for some unkown reason

	// // data := make([]float32, 38050)
	// // data = append(data, pcm_arr...)
	// // pcm_arr = data

	// // logger.Info("before encode") // REMOVE AFTER DEBUG

	// // ae.Encode(pcm_arr, 1, 22050) // Encode the pcm from Flask into opus frames and then into media samples. 22050 is the sample rate of pcm data from Flask server

	// // logger.Info("after encode") // REMOVE AFTER DEBUG
	// // wavFrames := ChunkWav(wav_arr, 22050)
	// // go ae.SendMediaWav(wavFrames)

	// err := ffmpeg.Input("pipe:0").
	// 	Output("pipe:1", ffmpeg.KwArgs{"c:a": "libopus", "page_duration": 2000, "ac": 2}).
	// 	Run()
	// if err != nil {
	// 	logger.Info("Error at ffmpeg.Input()!!", err)
	// }
	// // write wav_arr to std_in
	// _, err = os.Stdin.Write(wav_arr)
	// if err != nil {
	// 	logger.Info("Error at os.Stdin.Write()!!", err)
	// }
	// opus_byte_arr := make([]byte, 100000000)
	// n, err := os.Stdout.Read(opus_byte_arr)
	// if n > 0 {
	// 	logger.Info("length of wav bytes converted to opus:", n)
	// 	// inspiration for slicing valid part: https://stackoverflow.com/questions/43601846/golang-and-ffmpeg-realtime-streaming-input-output
	// 	valid_opus_byte_arr := opus_byte_arr[:n]
	// 	// chunk opus_byte_arr into frames
	// 	opusFrames := ChunkOpus(valid_opus_byte_arr, 22050)
	// 	// convert opus frames to media samples
	// 	go ae.SendMedia(opusFrames)
	// }

	// go rtc.ProcessOutgoingMedia()

	// // resume Ria listening
	// p.unpauseFunc()

	// *** End of sending currentPrompt to Flask server code ***

	// // If the state sent back by the Flask server is 4 then end the inference after 15s
	// if flaskResponse.New_state == 4 {
	// 	f := callkillGoClient(rtc)
	// 	time.AfterFunc(15*time.Second, f)
	// // }

	p.Lock()

	// no prompt so wait again
	if p.prompt == "" {
		p.Unlock()
		return
	}

	currentPrompt := p.prompt
	p.prompt = ""

	p.Unlock()

	// pause Ria  listening so we dont interrupt the response streaming
	p.pauseFunc()

	endpointURL := "http://localhost:8000/get_response_audio_pcm"
	logger.Info("The current_prompt being sent to Flask: ", currentPrompt)
	p.Lock() // locking since we're going to access p.currentState
	var jsonStrByte = []byte(`{"end_user_input": "` + currentPrompt + `", "curr_state":"` + strconv.Itoa(p.currentState) + `", "client_id":"1", "prompt_repeated_response":"0"}`)

	flaskResponsePcm := new(FlaskResponsePcm)
	logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	getJson(endpointURL, jsonStrByte, flaskResponsePcm)

	var err error // Declare the err variable
	p.currentState, err = strconv.Atoi(flaskResponsePcm.NewState)
	if err != nil {
		logger.Info("Error at strconv.Atoi()!!", err)
	}
	p.Unlock()

	// extract pcm array from json
	var pcm_str string = flaskResponsePcm.Audio
	logger.Info("Received pcm_str from Flask Server")
	logger.Info("pcm_str: ", pcm_str[0:100])

	// Remove brackets and split by commas
	pcmValuesStr := strings.Trim(pcm_str, "[]")
	pcmValuesStrArr := strings.Split(pcmValuesStr, ",")

	// Parse each string to float32
	var pcm_float_arr []float32
	for _, pcmValueStr := range pcmValuesStrArr {
		value, err := strconv.ParseFloat(strings.TrimSpace(pcmValueStr), 32)
		if err != nil {
			logger.Info("Error at strconv.ParseFloat()!!", err)
		}
		pcm_float_arr = append(pcm_float_arr, float32(value))
	}

	logger.Info("len(pcm_float_arr): ", len(pcm_float_arr))
	logger.Info("pcm_float_arr: ", pcm_float_arr[0:100])

	// encode pcmFrames to opus
	ae.Encode(pcm_float_arr, 1, 22050)

	go rtc.ProcessOutgoingMedia()
	// resume Ria listening
	p.unpauseFunc()

	// If the state sent back by the Flask server is 4 then end the inference after 15s
	if flaskResponsePcm.NewState == "4" {
		f := callkillGoClient(rtc)
		time.AfterFunc(15*time.Second, f)
	}
}

// type RequestBody struct {
// 	EndUserInput           string `json:"end_user_input"`
// 	CurrState              string `json:"curr_state"`
// 	ClientID               string `json:"client_id"`
// 	PromptRepeatedResponse string `json:"prompt_repeated_response"`
// }

// func fetchAudioFromEndpoint(endpointURL string, requestBody *RequestBody) ([]byte, error) {
// 	// Convert the JSON payload to a byte slice
// 	jsonData, err := json.Marshal(requestBody)
// 	if err != nil {
// 		logger.Info("Error at json.Marshal() inside fetchAudioFromEndpoint", err)
// 		return nil, err
// 	}

// 	// Make a POST request to the specified endpoint with the JSON payload
// 	response, err := http.Post(endpointURL, "application/json", bytes.NewBuffer(jsonData))
// 	if err != nil {
// 		logger.Info("Error at http.Post() inside fetchAudioFromEndpoint", err)
// 		return nil, err
// 	}
// 	defer response.Body.Close()

// 	// Check if the response status code is successful (200 OK)
// 	if response.StatusCode != http.StatusOK {
// 		logger.Info("Status not OK inside fetchAudioFromEndpoint", response.StatusCode)
// 		return nil, err
// 	}
// 	logger.Info("response body: ", response.Body)

// 	// Read the audio data from the response body
// 	// audioHexData, err := ioutil.ReadAll(response.Body)
// 	// if err != nil {
// 	// 	return nil, err
// 	// }
// 	// logger.Info("audioHexData: ", audioHexData)

// 	// // Convert hexadecimal audio data to decimal -- sliced from index 5 to remove the "RIFF$ " prefix
// 	// audioDecimalData, err := hex.DecodeString(string(audioHexData[5:]))
// 	// if err != nil {
// 	// 	return nil, err
// 	// }

// 	// return audioDecimalData, nil

// 	// Read the audio data from the response body
// 	audioData, err := ioutil.ReadAll(response.Body)
// 	if err != nil {
// 		return nil, err
// 	}

// 	return audioData, nil
// }

func riaSaysHello(ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) int {
	logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	// send POST req to the URL with user_input and get the json containing pcm
	// url := "http://localhost:8000/get_response"

	// // Sending curr_state 0 signal to flask along with a hard-coded hello (content of endu_user_input doesn't matter)
	// // This is to get the intro as response
	// var jsonStrByte = []byte(`{"end_user_input":"Hello!", "curr_state":"0", "client_id":"1", "prompt_repeated_response":"0"}`)

	// flaskResponse := new(FlaskResponse)
	// getJson(url, jsonStrByte, flaskResponse)

	// // extract pcm array from json
	// var wav_arr []byte = []byte(flaskResponse.Wav_arr)
	// new_state := flaskResponse.New_state
	// logger.Info("len(wav_arr): ", len(wav_arr))

	// // padding the audio with some silence -- this is important, without this the start of the audio gets cut off for some unkown reason

	// data := make([]float32, 38050)
	// data = append(data, pcm_arr...)
	// pcm_arr = data

	// logger.Info("before encode") // REMOVE AFTER DEBUG
	// // time.Sleep(100 * time.Millisecond)

	// ae.Encode(pcm_arr, 1, 22050) // Encode the pcm from Flask into opus frames and then into media samples. 22050 is the sample rate of pcm data from Flask server

	// logger.Info("after encode") // REMOVE AFTER DEBUG

	// Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG

	// wavFrames := ChunkWav(wav_arr, 22050)
	// go ae.SendMediaWav(wavFrames)

	// this endpoint returns standardized pcm data in the json format: {audio:"--pcm data--"}
	endpointURL := "http://localhost:8000/get_response_audio_pcm"

	var jsonStrByte = []byte(`{"end_user_input":"Hello!", "curr_state":"0", "client_id":"1", "prompt_repeated_response":"0"}`)

	flaskResponsePcm := new(FlaskResponsePcm)
	logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	getJson(endpointURL, jsonStrByte, flaskResponsePcm)

	logger.Info("flaskResponsePcm.NewState:", flaskResponsePcm.NewState)
	new_state, err := strconv.Atoi(flaskResponsePcm.NewState)
	if err != nil {
		logger.Info("Error at strconv.Atoi()!!", err)
	}

	// extract pcm array from json
	var pcm_str string = flaskResponsePcm.Audio
	logger.Info("Received pcm_str from Flask Server")
	logger.Info("pcm_str: ", pcm_str[0:100])

	// Remove brackets and split by commas
	pcmValuesStr := strings.Trim(pcm_str, "[]")
	pcmValuesStrArr := strings.Split(pcmValuesStr, ",")

	// Parse each string to float32
	var pcm_float_arr []float32
	for _, pcmValueStr := range pcmValuesStrArr {
		value, err := strconv.ParseFloat(strings.TrimSpace(pcmValueStr), 32)
		if err != nil {
			return -1
		}
		pcm_float_arr = append(pcm_float_arr, float32(value))
	}

	logger.Info("len(pcm_float_arr): ", len(pcm_float_arr))
	logger.Info("pcm_float_arr: ", pcm_float_arr[0:100])

	// // Create the JSON payload
	// requestBody := &RequestBody{
	// 	EndUserInput:           "Hello!",
	// 	CurrState:              "0",
	// 	ClientID:               "1",
	// 	PromptRepeatedResponse: "0",
	// }

	// // Fetch audio data from the specified endpoint with the JSON payload
	// audioData, err := fetchAudioFromEndpoint(endpointURL, requestBody)
	// if err != nil {
	// 	log.Fatal("Error fetching audio data:", err)
	// }

	// inBuf1 := bytes.NewBuffer(audioData)
	// outBuf1 := bytes.NewBuffer(nil)

	// logger.Info("Running ffmpeg")
	// err = ffmpeg.Input("pipe:").
	// 	WithInput(inBuf1).
	// 	// Output("pipe:", ffmpeg.KwArgs{"c:a": "pcm_s16le", "ar": 48000, "ac": 2, "f": "s16le"}).
	// 	Output("pipe:", ffmpeg.KwArgs{"acodec": "pcm_s16le", "ar": 22050, "ac": 2, "f": "s16le"}).
	// 	WithOutput(outBuf1).
	// 	Run()
	// logger.Info("contents of outBuf1: ", outBuf1.Bytes()[0:100])

	// pcm_bytes_arr := outBuf1.Bytes()

	// logger.Info("contents of pcm_bytes_arr: ", pcm_bytes_arr[0:100])

	// // convert pcm_bytes_arr from a byte array to float32 array, assuming pcm_bytes_arr is signed 16 bit little endian
	// pcm_float_arr := make([]float32, len(pcm_bytes_arr))
	// for i := 0; i < len(pcm_bytes_arr); i++ {
	// 	pcm_float_arr[i] = float32(pcm_bytes_arr[i])
	// 	// pcm_float_arr[i] = (pcm_float_arr[i] - float32(-11.71)) / float32(5481.07)
	// }

	// logger.Info("contents of pcm_float_arr: ", pcm_float_arr[0:100])
	// save pcm_float_arr to a file
	// err = ioutil.WriteFile("pcm_float_arr48KHz_fromFlask.pcm", []byte(fmt.Sprintf("%v", pcm_float_arr)), 0644)

	f, err := os.OpenFile("pcm_float_standardized_22050Hz.pcm",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Println(err)
	}
	for _, value := range pcm_float_arr {
		fmt.Fprintln(f, value) // print values to f, one per line
	}

	// encode pcmFrames to opus
	ae.Encode(pcm_float_arr, 1, 22050)

	// inBuf2 := bytes.NewBuffer(outBuf1.Bytes())
	// outBuf2 := bytes.NewBuffer(nil)
	// err = ffmpeg.Input("pipe:", ffmpeg.KwArgs{"ar": 48000, "ac": 2, "f": "f32le"}).
	// 	WithInput(inBuf2).
	// 	Output("pipe:", ffmpeg.KwArgs{"c:a": "libopus", "ar": 48000, "ac": 2, "f": "ogg"}).
	// 	WithOutput(outBuf2).
	// 	Run()
	// if err != nil {
	// 	logger.Info("Error at ffmpeg.Input()!!", err)
	// }
	// opus_byte_arr := outBuf2.Bytes()
	// logger.Info("Contents of opus_byte_arr: ", opus_byte_arr[0:100])
	// logger.Info("Length of opus_byte_arr: ", len(opus_byte_arr))
	// outBuf2.Reset()

	// // write the opus_byte_arr to a file
	// err = ioutil.WriteFile("opus_byte_arr48KHz_frompcm.ogg", opus_byte_arr, 0644)
	// if err != nil {
	// 	logger.Info("Error writing to file:", err)
	// }

	// // opusFrames := ChunkOpus(opus_byte_arr, 48000)
	// // logger.Info("Length of opusFrames: ", len(opusFrames))
	// // go ae.SendMedia(opusFrames)
	// go ae.SendMediaByteArr(opus_byte_arr)

	go rtc.ProcessOutgoingMedia()
	return new_state
}

// func sendStallMsg(ae *rtc_client.AudioEngine, rtc *rtc_client.RTCConnection) {
// 	logger.Info("CHOOSING stall message")
// 	randomIndex := rand.Intn(len(msgs))
// 	chosen_msg := msgs[randomIndex]

// 	// padding the audio with some silence -- this is important, without this the start of the audio gets cut off for some unkown reason

// 	data := make([]float32, 38050)
// 	data = append(data, chosen_msg...)
// 	chosen_msg = data

// 	logger.Info("SENDING stall message")

// 	ae.Encode(chosen_msg, 1, 22050) // Encode the pcm from Flask into opus frames and then into media samples. 22050 is the sample rate of pcm data from Flask server

// 	// Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
// 	go rtc.ProcessOutgoingMedia()
// }
