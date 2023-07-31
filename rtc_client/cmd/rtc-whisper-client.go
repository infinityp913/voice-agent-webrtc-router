package main

import (
	"net/url"
	"strings"

	logr "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/log"
	whisper "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/backends/whisper.cpp"
	"github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	stt "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	"github.com/infinityp913/rtc-go-server/rtc_client"
)

var (
	logger = logr.New()
)

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

	sc, err := rtc_client.NewRiaClient(rtc_client.RiaConfig{
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
