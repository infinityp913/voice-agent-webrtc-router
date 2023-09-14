package rtc_client

import (
	"errors"
	"net/url"

	logr "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/log"
	stt "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"

	// stt "github.com/infinityp913/rtc-go-server/stt/engine"
	"github.com/pion/webrtc/v3"
)

var Logger = logr.New()

type RiaConfig struct {
	// ION room name to connect to
	Room string
	// URL for websocket server
	Url url.URL
	// STT engine to generate transcriptions
	SttEngine *stt.Engine

	// channel used to send transcription segments over the data channel
	// any transcription segment sent on this channel with be sent over the data channel
	TranscriptionStream chan stt.Document
}

type RiaClient struct {
	ws     *SocketConnection
	rtc    *RTCConnection
	config RiaConfig
	ae     *AudioEngine
}

func NewRiaClient(config RiaConfig) (*RiaClient, error) {
	// TODO allow this to be nil and just disable transcriptions in that case
	if config.SttEngine == nil {
		return nil, errors.New("SttEngine cannot be nil")
	}
	ae, err := NewAudioEngine(config.SttEngine)
	if err != nil {
		return nil, err
	}

	ws := NewSocketConnection(config.Url)

	rtc, err := NewRTCConnection(RTCConnectionParams{
		trickleFn: func(candidate *webrtc.ICECandidate, target int) error {
			return ws.SendTrickle(candidate, target)
		},
		rtpChan:             ae.RtpIn(),
		transcriptionStream: config.TranscriptionStream,
		// transcriptionStream: nil,
		mediaIn: ae.MediaOut(),
	})
	if err != nil {
		return nil, err
	}

	// take transcriptions from rtc.transcriptionStream
	// feed them into ae.Encode()
	// do `go rtc.processOutgoingMedia` this will trigger writing to rtc.audioTrack

	Logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	// send POST req to the URL with user_input and get the json containing pcm
	url := "http://localhost:8000/get_tts"
	var jsonStrByte = []byte(`{"text":"Hello! Its so nice to meet you!! Im excited about the work we are gonna get done today. Cant wait to get started! Alright, so what do you really want to do today? Im free after 5pm. Oh you dont wanna do kayaking? Thats a shame"}`)

	flaskResponse := new(FlaskResponse)
	getJson(url, jsonStrByte, flaskResponse)

	// extract pcm array from json
	var pcm_arr []float32 = flaskResponse.Pcm_arr
	Logger.Info("len(pcm_arr): ", len(pcm_arr))

	// padding the audio with some silence -- seeing if this fixes the partial audio problem

	// data := make([]float32, 4800)
	// data = append(data, pcm_arr...)
	// pcm_arr = data

	// // Chunking pcm_arr before passing to ae.Encode()
	// var chunked_pcm_arr [][]float32

	// chunksize := 4700

	// for i := 0; i < len(pcm_arr); i += chunksize {
	// 	end := i + chunksize

	// 	if end > len(pcm_arr) {
	// 		end = len(pcm_arr)
	// 	}
	// 	chunked_pcm_arr = append(chunked_pcm_arr, pcm_arr[i:end])

	// }

	Logger.Info("before encode") // REMOVE AFTER DEBUG
	// Logger.Info("total # of chunks", len(chunked_pcm_arr))
	// // Looping through the chunks
	// for i, chunk := range chunked_pcm_arr {

	// 	Logger.Info("len(chunk): ", len(chunk))
	// 	Logger.Info("chunk #", i) // REMOVE AFTER DEBUG

	// 	// pass it to ae.Encode(), where the pcm array is encoded to Opus frames AND
	// 	// they're sent over to the browser via WebRTC using the processOutgoingMedia() function in AudioEngine
	// 	ae.Encode(chunk, 1, 22050)
	// 	Logger.Info("After each encode") // REMOVE AFTER DEBUG

	// 	Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
	// 	go rtc.processOutgoingMedia()
	// }

	// Trying to send a 1s chunk
	// Logger.Info("len of chunk: ", len(pcm_arr[2*len(pcm_arr)/3:len(pcm_arr)]))
	// ae.Encode(pcm_arr[2*len(pcm_arr)/3:len(pcm_arr)], 1, 22050)

	ae.Encode(pcm_arr, 1, 22050)

	Logger.Info("after encode") // REMOVE AFTER DEBUG

	// Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
	go rtc.processOutgoingMedia()

	r := &RiaClient{
		ws:     ws,
		rtc:    rtc,
		config: config,
		ae:     ae,
	}

	r.ws.SetOnOffer(r.OnOffer)
	r.ws.SetOnAnswer(r.OnAnswer)
	r.ws.SetOnTrickle(r.rtc.OnTrickle)

	return r, nil
}

func (r *RiaClient) OnAnswer(answer webrtc.SessionDescription) error {
	return r.rtc.SetAnswer(answer)
}

func (r *RiaClient) OnOffer(offer webrtc.SessionDescription) error {
	ans, err := r.rtc.OnOffer(offer)
	if err != nil {
		Logger.Error(err, "error getting answer")
		return err
	}

	return r.ws.SendAnswer(ans)
}

func (r *RiaClient) Start() error {
	Logger.Info("before ws.connect")
	if err := r.ws.Connect(); err != nil {
		Logger.Error(err, "error connecting to websocket")
		return err
	}
	Logger.Info("before rtc.GetOffer")
	offer, err := r.rtc.GetOffer()
	if err != nil {
		Logger.Error(err, "error getting intial offer")
	}
	Logger.Info("before ws.join")
	if err := r.ws.Join(r.config.Room, offer); err != nil {
		Logger.Error(err, "error joining room")
		return err
	}

	r.ae.Start()

	r.ws.WaitForDone()
	Logger.Info("Socket done goodbye")
	return nil
}
