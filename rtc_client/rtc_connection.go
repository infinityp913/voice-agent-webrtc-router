package rtc_client

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	"github.com/infinityp913/rtc-go-server/rtc_client/internal"

	// "github.com/infinityp913/rtc-go-server/stt/engine"

	"strings" //REMOVE

	whisper "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/backends/whisper.cpp" //REMOVE

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

type RTCConnection struct {
	sub PeerConn
	pub PeerConn
	// channel that incoming audio rtp packets will be relayed on
	rtpIn chan<- *rtp.Packet
	// channel to send outgoing audio samples to
	mediaIn    <-chan media.Sample
	audioTrack *webrtc.TrackLocalStaticSample
}

type RTCConnectionParams struct {
	trickleFn           func(*webrtc.ICECandidate, int) error
	rtpChan             chan<- *rtp.Packet
	transcriptionStream <-chan engine.Document
	mediaIn             <-chan media.Sample
}

// FIXME if transcriptionStream AND mediaIn are not provided this will blow up
func NewRTCConnection(params RTCConnectionParams) (*RTCConnection, error) {
	rtc := &RTCConnection{
		rtpIn:   params.rtpChan,
		mediaIn: params.mediaIn,
	}

	rtc.sub = NewPeerConn(func(candidate *webrtc.ICECandidate) {
		params.trickleFn(candidate, 1)
	})
	rtc.sub.conn.OnTrack(func(t *webrtc.TrackRemote, r *webrtc.RTPReceiver) {
		kind := "unknown kind"
		if t.Kind() == webrtc.RTPCodecTypeVideo {
			kind = "video"
		} else if t.Kind() == webrtc.RTPCodecTypeAudio {
			kind = "audio"
			go func() {
				for {
					pkt, _, err := t.ReadRTP()
					if err != nil {
						internal.Logger.Error(err, "err reading rtp")
						return
					}
					rtc.rtpIn <- pkt
				}
			}()
		}
		internal.Logger.Debugf("got track %s", kind)
	})

	rtc.pub = NewPeerConn(func(candidate *webrtc.ICECandidate) {
		params.trickleFn(candidate, 0)
	})

	if params.mediaIn != nil {
		audioTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "saturday_audio")
		if err != nil {
			internal.Logger.Error(err, "error creating local audio track")
			return nil, err
		}

		_, err = rtc.pub.conn.AddTransceiverFromTrack(audioTrack, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			internal.Logger.Error(err, "error adding local audio transceiver")
			return nil, err
		}

		rtc.audioTrack = audioTrack

		go rtc.processOutgoingMedia()
	} else {
		internal.Logger.Info("mediaIn not provided... audio relay is disabled")
	}

	if params.transcriptionStream != nil {
		ordered := true
		maxRetransmits := uint16(0)

		dc, err := rtc.pub.conn.CreateDataChannel(
			"transcriptions",
			&webrtc.DataChannelInit{
				Ordered:        &ordered,
				MaxRetransmits: &maxRetransmits,
			})
		if err != nil {
			return nil, err
		}

		dc.OnOpen(func() {
			internal.Logger.Info("data channel opened...")

			for transcription := range params.transcriptionStream {
				internal.Logger.Debugf("Transcribed %s", transcription.TranscribedText)
				internal.Logger.Debugf("New text %s", transcription.NewText)
				internal.Logger.Info("Transcribed %s", transcription.TranscribedText)
				internal.Logger.Info("New text %s", transcription.NewText)
				data, err := json.Marshal(transcription)
				if err != nil {
					internal.Logger.Error(err, "error marshalling transcript")
					continue
				}
				internal.Logger.Debugf("sending transcript %+v on data channel", transcription)
				dc.Send(data)
			}
			internal.Logger.Info("Getting PCM data from Flask Server") // REMOVE AFTER DEBUG
			// send POST req to the URL with user_input and get the json containing pcm
			url := "http://localhost:8000/get_response"
			var jsonStrByte = []byte(`{"end_user_input":"oh okay, thanks.", "curr_state":"4", "client_id":"1", "prompt_repeated_response":"0"}`)

			flaskResponse := new(FlaskResponse)
			getJson(url, jsonStrByte, flaskResponse)

			// extract pcm array from json
			var pcm_arr []float32 = flaskResponse.Pcm_arr
			internal.Logger.Info("Inside audio-engine.go: fR.pcmarr:", flaskResponse.Pcm_arr)
			internal.Logger.Info("Inside audio-engine.go: pcm_arr:", pcm_arr)
			internal.Logger.Info("Inside audio-engine.go: New_state:", flaskResponse.New_state)

			dec, err := internal.NewOpusDecoder(sampleRate, channels)
			if err != nil {
				internal.Logger.Error(err, "error creating decoder for Audio Engine in the Data Channel for Transcriptions")
			}

			// we use 2 channels for the output
			enc, err := internal.NewOpusEncoder(2, frameSizeMs)
			if err != nil {
				internal.Logger.Error(err, "error creating decoder for Audio Engine in the Data Channel for Transcriptions")
			}

			documentComposer := engine.NewDocumentComposer()
			documentComposer.FilterSegment(func(ts engine.TranscriptionSegment) bool {
				return ts.Text[0] == '.' || strings.ContainsAny(ts.Text, "[]()")
			})

			whisperCpp, err := whisper.New("../models/ggml-base.en.bin")
			if err != nil {
				internal.Logger.Fatal(err, "error creating whisper model")
			}

			sttEngine, err := engine.New(engine.EngineParams{
				Transcriber:      whisperCpp,
				DocumentComposer: documentComposer,
				UseVad:           true,
			})

			ae := &AudioEngine{
				rtpIn:          make(chan *rtp.Packet),
				mediaOut:       make(chan media.Sample),
				pcm:            make([]float32, frameSize),
				buf:            make([]byte, frameSize*2),
				dec:            dec,
				enc:            enc,
				sttEngine:      sttEngine,
				firstTimeStamp: 0,
			}

			internal.Logger.Info("before encode") // REMOVE AFTER DEBUG
			// pass it to ae.Encode()
			ae.Encode(pcm_arr, 1, 22050)
			internal.Logger.Info("After encode") // REMOVE AFTER DEBUG
		})

	} else {
		internal.Logger.Info("transcriptionStream not provided... transcription relay is disabled")
	}

	return rtc, nil
}

// processIncomingMedia sends the provided samples on the audioTrack
func (r *RTCConnection) processOutgoingMedia() {
	if r.mediaIn == nil {
		internal.Logger.Info("MediaIn not provided... skipping relay")
		return
	}
	for sample := range r.mediaIn {
		if err := r.audioTrack.WriteSample(sample); err != nil {
			internal.Logger.Error(err, "error writing sample")
		}
	}
}

func (r *RTCConnection) OnTrickle(candidate webrtc.ICECandidateInit, target int) error {
	switch target {
	case 0:
		return r.pub.AddIceCandidate(candidate)
	case 1:
		return r.sub.AddIceCandidate(candidate)
	default:
		err := errors.New(fmt.Sprintf("unknown target %d for candidate", target))
		internal.Logger.Error(err, "error OnTrickle")
		return err
	}
}

func (r *RTCConnection) GetOffer() (webrtc.SessionDescription, error) {
	return r.pub.GetOffer()
}

func (r *RTCConnection) SetAnswer(answer webrtc.SessionDescription) error {
	return r.pub.SetAnswer(answer)
}

func (r *RTCConnection) OnOffer(offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	var answer = webrtc.SessionDescription{}
	if err := r.sub.Offer(offer); err != nil {
		internal.Logger.Error(err, "error setting offer")
		return answer, err
	}

	answer, err := r.sub.Answer()
	if err != nil {
		internal.Logger.Error(err, "error getting answer")
		return answer, err
	}
	return answer, nil
}
