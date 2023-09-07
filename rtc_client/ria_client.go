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

	s := &RiaClient{
		ws:     ws,
		rtc:    rtc,
		config: config,
		ae:     ae,
	}

	s.ws.SetOnOffer(s.OnOffer)
	s.ws.SetOnAnswer(s.OnAnswer)
	s.ws.SetOnTrickle(s.rtc.OnTrickle)

	return s, nil
}

func (s *RiaClient) OnAnswer(answer webrtc.SessionDescription) error {
	return s.rtc.SetAnswer(answer)
}

func (s *RiaClient) OnOffer(offer webrtc.SessionDescription) error {
	ans, err := s.rtc.OnOffer(offer)
	if err != nil {
		Logger.Error(err, "error getting answer")
		return err
	}

	return s.ws.SendAnswer(ans)
}

func (s *RiaClient) Start() error {
	Logger.Info("before ws.connect")
	if err := s.ws.Connect(); err != nil {
		Logger.Error(err, "error connecting to websocket")
		return err
	}
	Logger.Info("before rtc.GetOffer")
	offer, err := s.rtc.GetOffer()
	if err != nil {
		Logger.Error(err, "error getting intial offer")
	}
	Logger.Info("before ws.join")
	if err := s.ws.Join(s.config.Room, offer); err != nil {
		Logger.Error(err, "error joining room")
		return err
	}

	// take transcriptions from rtc.transcriptionStream
	// feed them into ae.Encode()
	// do `go rtc.processOutgoingMedia` this will trigger writing to rtc.audioTrack

	Logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	// send POST req to the URL with user_input and get the json containing pcm
	url := "http://localhost:8000/get_response"
	var jsonStrByte = []byte(`{"end_user_input":"oh okay, thanks.", "curr_state":"4", "client_id":"1", "prompt_repeated_response":"0"}`)

	flaskResponse := new(FlaskResponse)
	getJson(url, jsonStrByte, flaskResponse)

	// extract pcm array from json
	var pcm_arr []float32 = flaskResponse.Pcm_arr
	Logger.Info("len(pcm_arr): ", len(pcm_arr))

	// Chunking pcm_arr before passing to ae.Encode()
	var chunked_pcm_arr [][]float32

	chunksize := 4800

	for i := 0; i < len(pcm_arr); i += chunksize {
		end := i + chunksize

		if end > len(pcm_arr) {
			end = len(pcm_arr)
		}
		chunked_pcm_arr = append(chunked_pcm_arr, pcm_arr[i:end])

	}

	Logger.Info("before encode") // REMOVE AFTER DEBUG

	// Looping through the chunks
	for _, chunk := range chunked_pcm_arr {

		Logger.Info("len(chunk): ", len(chunk))

		// pass it to ae.Encode(), where the pcm array is encoded to Opus frames AND
		// they're sent over to the browser via WebRTC using the processOutgoingMedia() function in AudioEngine
		s.ae.Encode(chunk, 1, 22050)
		Logger.Info("After each encode") // REMOVE AFTER DEBUG

		Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
		s.rtc.processOutgoingMedia()
	}

	s.ae.Start()

	s.ws.WaitForDone()
	Logger.Info("Socket done goodbye")
	return nil
}
