package rtc_client

import (
	"os"
	"sync"

	"github.com/infinityp913/rtc-go-server/rtc_client/internal"
	log "github.com/pion/ion-sfu/pkg/logger"
	"github.com/pion/webrtc/v3"
)

type PeerConn struct {
	conn              *webrtc.PeerConnection
	pendingCandidates []webrtc.ICECandidateInit
	mu                sync.Mutex
}

var (
	logger = log.New()
)

// to unmarshal the json to get u and p
type User struct {
	Username string `json:"user"`
	Pass     string `json:"pass"`
}

func NewPeerConn(onICECandidate func(candidate *webrtc.ICECandidate)) PeerConn {
	// Prepare the configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
				// URLs: []string{"stun:stun.relay.metered.ca:80"},
			},
		},
	}

	// Create a new RTCPeerConnection
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		internal.Logger.Fatal(err, "pc err")
	}

	pc := PeerConn{
		conn:              peerConnection,
		pendingCandidates: make([]webrtc.ICECandidateInit, 0),
	}

	// When an ICE candidate is available send to the other Pion instance
	// the other Pion instance will add this candidate by calling AddICECandidate
	peerConnection.OnICECandidate(onICECandidate)

	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {

		if s == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure. It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			os.Exit(0)
		}
	})

	return pc
	// defer func() {
	// 	if err := peerConnection.Close(); err != nil {
	// 		fmt.Printf("cannot close peerConnection: %v\n", err)
	// 	}
	// }()
}

func (c PeerConn) Offer(offer webrtc.SessionDescription) error {
	return c.conn.SetRemoteDescription(offer)
}

func (c PeerConn) Answer() (webrtc.SessionDescription, error) {
	var answer = webrtc.SessionDescription{}

	answer, err := c.conn.CreateAnswer(nil)
	if err != nil {
		return answer, err
	}
	if err = c.conn.SetLocalDescription(answer); err != nil {
		return answer, err
	}

	if err = c.flushCandidates(); err != nil {
		internal.Logger.Error(err, "error flushing candidates in Answer")
	}

	return answer, nil
}

func (c PeerConn) flushCandidates() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, candidate := range c.pendingCandidates {
		if err := c.conn.AddICECandidate(candidate); err != nil {
			internal.Logger.Errorf(err, "error adding ice candidate %+v", candidate)
			return err
		}
	}
	c.pendingCandidates = make([]webrtc.ICECandidateInit, 0)
	return nil
}

func (c PeerConn) GetOffer() (webrtc.SessionDescription, error) {
	var offer = webrtc.SessionDescription{}
	offer, err := c.conn.CreateOffer(nil)
	if err != nil {
		return offer, err
	}
	return offer, c.conn.SetLocalDescription(offer)
}

func (c PeerConn) SetAnswer(answer webrtc.SessionDescription) error {
	if err := c.conn.SetRemoteDescription(answer); err != nil {
		return err
	}

	if err := c.flushCandidates(); err != nil {
		internal.Logger.Error(err, "error flushing candidates in SetAnswer")
	}
	return nil
}

func (c PeerConn) AddIceCandidate(candidate webrtc.ICECandidateInit) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// we got a candiate before the offer here so buffer
	if c.conn.RemoteDescription() == nil {
		c.pendingCandidates = append(c.pendingCandidates, candidate)
		return nil
	} else {
		return c.conn.AddICECandidate(candidate)
	}
}
