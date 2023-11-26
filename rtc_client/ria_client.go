package rtc_client

import (
	"errors"
	"net/url"

	logr "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/log"
	// stt "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"

	stt "github.com/infinityp913/rtc-go-server/stt/engine"
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
	Rtc    *RTCConnection
	config RiaConfig
	Ae     *AudioEngine
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

	// Logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
	// // send POST req to the URL with user_input and get the json containing pcm
	// url := "http://localhost:8000/get_response"
	// var jsonStrByte = []byte(`{"end_user_input":"Hello.", "curr_state":"0", "client_id":"1", "prompt_repeated_response":"0"}`)

	// flaskResponse := new(FlaskResponse)
	// getJson(url, jsonStrByte, flaskResponse)

	// // extract pcm array from json
	// var pcm_arr []float32 = flaskResponse.Pcm_arr
	// Logger.Info("len(pcm_arr): ", len(pcm_arr))

	// // padding the audio with some silence -- seeing if this fixes the partial audio problem

	// data := make([]float32, 38050)
	// data = append(data, pcm_arr...)
	// pcm_arr = data

	// Logger.Info("before encode") // REMOVE AFTER DEBUG

	// ae.Encode(pcm_arr, 1, 22050)

	// Logger.Info("after encode") // REMOVE AFTER DEBUG

	// // Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
	// go rtc.ProcessOutgoingMedia()

	r := &RiaClient{
		ws:     ws,
		Rtc:    rtc,
		config: config,
		Ae:     ae,
	}

	r.ws.SetOnOffer(r.OnOffer)
	r.ws.SetOnAnswer(r.OnAnswer)
	r.ws.SetOnTrickle(r.Rtc.OnTrickle)

	return r, nil
}

func (r *RiaClient) PauseRia() {
	r.Ae.Pause()
}

func (r *RiaClient) UnpauseRia() {
	r.Ae.Unpause()
}

func (r *RiaClient) OnAnswer(answer webrtc.SessionDescription) error {
	return r.Rtc.SetAnswer(answer)
}

func (r *RiaClient) OnOffer(offer webrtc.SessionDescription) error {
	ans, err := r.Rtc.OnOffer(offer)
	if err != nil {
		Logger.Error(err, "error getting answer")
		return err
	}

	return r.ws.SendAnswer(ans)
}

func (r *RiaClient) Start() error {

	// Setting up the Websocket connection

	Logger.Info("before ws.connect")
	if err := r.ws.Connect(); err != nil {
		Logger.Error(err, "error connecting to websocket")
		return err
	}
	Logger.Info("before rtc.GetOffer")
	offer, err := r.Rtc.GetOffer()
	if err != nil {
		Logger.Error(err, "error getting intial offer")
	}
	Logger.Info("before ws.join")
	if err := r.ws.Join(r.config.Room, offer); err != nil {
		Logger.Error(err, "error joining room")
		return err
	}

	// Starting the Media Reception (sending is done by tryCallEngine and riaSaysHello() in rtc-whisper-client)

	r.Ae.Start()

	r.ws.WaitForDone()
	Logger.Info("Socket done goodbye")
	return nil
}

func (r *RiaClient) CreateOfferAndSetLocalDescription() error {

	// Setting up the Websocket connection

	Logger.Info("before ws.connect")
	if err := r.ws.Connect(); err != nil {
		Logger.Error(err, "error connecting to websocket")
		return err
	}
	// Logger.Info("before rtc.GetOffer")
	// offer, err := r.Rtc.GetOffer()
	// if err != nil {
	// 	Logger.Error(err, "error getting intial offer")
	// }
	// Logger.Info("before ws.join")
	// if err := r.ws.Join(r.config.Room, offer); err != nil {
	// 	Logger.Error(err, "error joining room")
	// 	return err
	// }

	// Logger.Info("before ws.connect")
	// if err := r.ws.Connect(); err != nil {
	// 	Logger.Error(err, "error connecting to websocket")
	// 	return err
	// }

	// Create an offer
	offer, err := r.Rtc.GetOffer() // GetOffer does both CreateOffer and SetLocalDescription
	if err != nil {
		return err
	}

	// // DEBUG
	// Logger.Info("Offer: ", offer)
	// // END OF DEBUG

	//send offer to remote peer
	if err := r.ws.Join(r.config.Room, offer); err != nil {
		Logger.Error(err, "error joining room")
		return err
	} // Join sends the offer to the remote peer as well as run readMessages() in a goroutine

	r.ws.WaitForDone()
	Logger.Info("Socket done goodbye")

	return nil
}
