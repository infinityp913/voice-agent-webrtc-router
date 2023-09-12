package rtc_client

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	"github.com/infinityp913/rtc-go-server/rtc_client/internal"

	// "github.com/infinityp913/rtc-go-server/stt/engine"

	//REMOVE

	//REMOVE

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
		internal.Logger.Info("executing if params.MediaIn != nil") // REMOVE AFTER DEBUG
		audioTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "ria_audio")
		if err != nil {
			internal.Logger.Error(err, "error creating local audio track")
			return nil, err
		}

		_, err = rtc.pub.conn.AddTransceiverFromTrack(audioTrack, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			internal.Logger.Error(err, "error adding local audio transceiver")
			return nil, err
		}

		internal.Logger.Info("Added Transciever") // REMOVE AFTER DEBUG

		rtc.audioTrack = audioTrack

		// REMOVED POM FROM HERE
		// go rtc.processOutgoingMedia()

		// internal.Logger.Info("Executed processOutgoingMedia") // REMOVE AFTER DEBUG
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
				internal.Logger.Debugf("Transcribed debug %s", transcription.TranscribedText)
				internal.Logger.Debugf("New text debug %s", transcription.NewText)
				internal.Logger.Info("Transcribed info %s", transcription.TranscribedText)
				internal.Logger.Info("New text info %s", transcription.NewText)
				data, err := json.Marshal(transcription)
				if err != nil {
					internal.Logger.Error(err, "error marshalling transcript")
					continue
				}
				internal.Logger.Debugf("sending transcript %+v on data channel", transcription)
				dc.Send(data)
			}
		})

	} else {
		internal.Logger.Info("transcriptionStream not provided... transcription relay is disabled")
	}

	return rtc, nil
}

// processOutgoingMedia sends the provided samples on the audioTrack
func (r *RTCConnection) processOutgoingMedia() {
	internal.Logger.Info("Inside processOutgoingMedia")
	if r.mediaIn == nil {
		internal.Logger.Info("MediaIn not provided... skipping relay")
		return
	}
	internal.Logger.Info("TOTAL Number of samples to be written to rtc.audioTrack:", len(r.mediaIn))
	i := 0
	for sample := range r.mediaIn {
		i += 1
		internal.Logger.Info("MediaIn provided... writing samples from MediaIn (inside the sample:=loop)") // REMOVE AFTER DEBUG
		if err := r.audioTrack.WriteSample(sample); err != nil {
			internal.Logger.Error(err, "error writing sample") // REMOVE AFTER DEBUG
		}
		internal.Logger.Info("Number of samples written to rtc.audioTrack:", i)
	}
	internal.Logger.Info("FINAL Number of samples written to rtc.audioTrack:", i)
	internal.Logger.Info("Samples RECEIVED from MediaIn and written to rtc.audioTrack")
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
