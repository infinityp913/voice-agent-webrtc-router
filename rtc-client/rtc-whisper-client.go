package main

import (
	"main/rtcutils"
	"net/url"
	"strings"

	logr "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/log"
	whisper "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/backends/whisper.cpp"
	"github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	stt "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	"github.com/pion/webrtc/v3"
)

var (
	logger = logr.New()
)

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
	ws     *rtcutils.SocketConnection
	rtc    *RTCConnection
	config RiaConfig
	ae     *rtcutils.AudioEngine
}

func NewRiaClient(config RiaConfig) (*RiaClient, error) {
	ae, err := rtcutils.NewAudioEngine(config.SttEngine)
	if err != nil {
		return nil, err
	}

	ws := rtcutils.NewSocketConnection(config.Url)

	rtc, err := NewRTCConnection(RTCConnectionParams{
		trickleFn: func(candidate *webrtc.ICECandidate, target int) error {
			return ws.SendTrickle(candidate, target)
		},
		rtpChan: ae.RtpIn(),
		// transcriptionStream: config.TranscriptionStream,
		transcriptionStream: nil,
		mediaIn:             ae.MediaOut(),
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
		logger.Error(err, "error getting answer")
		return err
	}

	return s.ws.SendAnswer(ans)
}

func (s *RiaClient) Start() error {
	if err := s.ws.Connect(); err != nil {
		logger.Error(err, "error connecting to websocket")
		return err
	}
	offer, err := s.rtc.GetOffer()
	if err != nil {
		logger.Error(err, "error getting intial offer")
	}
	if err := s.ws.Join(s.config.Room, offer); err != nil {
		logger.Error(err, "error joining room")
		return err
	}

	// s.ae.Start()

	s.ws.WaitForDone()
	logger.Info("Socket done goodbye")
	return nil
}

func main() {
	url := url.URL{Scheme: "ws", Host: "matherium.com", Path: "/go-server"}

	whisperCpp, err := whisper.New("../models/ggml-tiny.en.bin")
	if err != nil {
		logger.Fatal(err, "error creating whisper model")
	}

	transcriptionStream := make(chan engine.Document, 100)

	documentComposer := stt.NewDocumentComposer()
	documentComposer.FilterSegment(func(ts stt.TranscriptionSegment) bool {
		return ts.Text[0] == '.' || strings.ContainsAny(ts.Text, "[]()")
	})

	sttEngine, err := stt.New(stt.EngineParams{
		Transcriber:      whisperCpp,
		DocumentComposer: documentComposer,
		UseVad:           true,
	})

	sc, err := NewRiaClient(RiaConfig{
		Room:                "",
		Url:                 url,
		SttEngine:           sttEngine,
		TranscriptionStream: transcriptionStream,
	})
	if err != nil {
		logger.Fatal(err, "error creating saturday client")
	}

	onDocumentUpdate := func(document engine.Document) {
		transcriptionStream <- document
	}

	sttEngine.OnDocumentUpdate(onDocumentUpdate)

	logger.Info("Starting Saturday Client...")

	if err := sc.Start(); err != nil {
		logger.Fatal(err, "error starting Saturday Client")
	}
}

