package rtc_client

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	// "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"

	"github.com/infinityp913/rtc-go-server/stt/engine"

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

	// channel to indicate the browser that Ria hung up
	Hungup chan int

	// channel to signal the browser to start the Browser Client
	StartBrowserClient chan int

	sync.Mutex // mutual exclusion lib to lock and unlock access to `prompt` by goroutines
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
		rtpIn:              params.rtpChan,
		mediaIn:            params.mediaIn,
		Hungup:             make(chan int),
		StartBrowserClient: make(chan int),
	}

	rtc.sub = NewPeerConn(func(candidate *webrtc.ICECandidate) {
		params.trickleFn(candidate, 1)
	})
	rtc.sub.conn.OnTrack(func(t *webrtc.TrackRemote, r *webrtc.RTPReceiver) {
		if t.Kind() == webrtc.RTPCodecTypeVideo {
		} else if t.Kind() == webrtc.RTPCodecTypeAudio {
			go func() {
				for {
					pkt, _, err := t.ReadRTP()
					if err != nil {
						return
					}
					rtc.rtpIn <- pkt
				}
			}()
		}
	})

	rtc.pub = NewPeerConn(func(candidate *webrtc.ICECandidate) {
		params.trickleFn(candidate, 0)
	})

	if params.mediaIn != nil {
		audioTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "ria_audio")
		if err != nil {
			return nil, err
		}

		_, err = rtc.pub.conn.AddTransceiverFromTrack(audioTrack, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			return nil, err
		}

		rtc.audioTrack = audioTrack

		// go rtc.ProcessOutgoingMedia()

	} else {
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

			for transcription := range params.transcriptionStream {
				data, err := json.Marshal(transcription)
				if err != nil {
					continue
				}
				dc.Send(data)
			}
		})

	} else {
	}

	return rtc, nil
}

// Function to send a signal to the browser to indicate that the Go client is about to be exited via os.exit()
func (rtc *RTCConnection) SendHangupSignal() {
	// Data channel to indicate to the browser that Ria hiung up aka Go client was exited via os.exit()
	maxRetransmits := uint16(0)
	ria_hungup_dc, err := rtc.pub.conn.CreateDataChannel(
		"ria-hungup",
		&webrtc.DataChannelInit{
			MaxRetransmits: &maxRetransmits,
		})
	if err != nil {
		return
	}
	ria_hungup_dc.OnOpen(func() {
		select {
		case <-rtc.Hungup:
			ria_hungup_dc.Send([]byte{1})
		}
	})
}

// Function to signal the browser to start the Browser Client
func (rtc *RTCConnection) SendStartBClientSignal() {
	// Data channel to signal the browser to start the Browser Client
	maxRetransmits := uint16(0)
	bclient_start_dc, err := rtc.pub.conn.CreateDataChannel(
		"bclient-start",
		&webrtc.DataChannelInit{
			MaxRetransmits: &maxRetransmits,
		})
	if err != nil {
		return
	}
	bclient_start_dc.OnOpen(func() {
		select {
		case <-rtc.StartBrowserClient:
			bclient_start_dc.Send([]byte{1})
		}
	})
}

// processOutgoingMedia sends the provided samples on the audioTrack
func (r *RTCConnection) ProcessOutgoingMedia() {

	if r.mediaIn == nil {
		return
	}
	i := 0
Loop:
	for sample := range r.mediaIn {
		if sample.Data == nil {
			break Loop
		}
		i += 1
		if err := r.audioTrack.WriteSample(sample); err != nil {
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
		return answer, err
	}

	answer, err := r.sub.Answer()
	if err != nil {
		return answer, err
	}
	return answer, nil
}
