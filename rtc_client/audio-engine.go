package rtc_client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	// stt "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	"github.com/infinityp913/rtc-go-server/rtc_client/internal"

	stt "github.com/infinityp913/rtc-go-server/stt/engine"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	sampleRate  = stt.SampleRate // (16000)
	channels    = 1              // decode into 1 channel since that is what whisper.cpp wants
	frameSizeMs = 20
)

var frameSize = channels * frameSizeMs * sampleRate / 1000

// AudioEngine is used to convert RTP Opus packets to raw PCM audio to be sent to Whisper
// and to convert raw PCM audio from the Flask server back to RTP Opus packets to be sent back over WebRTC
type AudioEngine struct {
	// RTP Opus packets to be converted to PCM
	rtpIn chan *rtp.Packet
	// RTP Opus packets converted from PCM to be sent over WebRTC
	mediaOut chan media.Sample

	dec *internal.OpusDecoder
	enc *internal.OpusEncoder
	// slice to hold raw pcm data during decoding
	pcm []float32
	// slice to hold binary encoded pcm data
	buf []byte

	firstTimeStamp uint32
	sttEngine      *stt.Engine

	// shouldInfer determines if we should run listen for end user speech i.e., accept RTP packets from browser client ot not
	shouldInfer atomic.Bool

	sync.Mutex // mutual exclusion lib to lock and unlock access to `prompt` by goroutines
}

type FlaskResponse struct {
	// New_state string    `json:"new_state"`
	Pcm_arr []float32 `json:"response"`
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
		panic(err)
	}

	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(target)
}

func NewAudioEngine(sttEngine *stt.Engine) (*AudioEngine, error) {
	dec, err := internal.NewOpusDecoder(sampleRate, channels)
	if err != nil {
		return nil, err
	}

	// we use 2 channels for the output
	enc, err := internal.NewOpusEncoder(2, frameSizeMs)
	if err != nil {
		return nil, err
	}

	var shouldInfer atomic.Bool
	shouldInfer.Store(true)

	ae := &AudioEngine{
		rtpIn:          make(chan *rtp.Packet),
		mediaOut:       make(chan media.Sample),
		pcm:            make([]float32, frameSize),
		buf:            make([]byte, frameSize*2),
		dec:            dec,
		enc:            enc,
		sttEngine:      sttEngine,
		firstTimeStamp: 0,
		shouldInfer:    shouldInfer,
	}

	return ae, nil
}

func (a *AudioEngine) RtpIn() chan<- *rtp.Packet {
	return a.rtpIn
}

func (a *AudioEngine) MediaOut() <-chan media.Sample {
	return a.mediaOut
}

func (a *AudioEngine) Start() {
	internal.Logger.Info("Starting audio engine")
	go a.decode()
}

// Pause stops the text to speech inference and simply drops incoming packets
func (a *AudioEngine) Pause() {
	Logger.Info("Pausing tts")
	a.shouldInfer.Swap(false)
}

// Unpause restarts the text to speech inference
func (a *AudioEngine) Unpause() {
	Logger.Info("Unpausing tts")
	a.shouldInfer.Swap(true)
}

// Encode takes in raw f32le pcm, encodes it into opus RTP packets and sends those over the rtpOut chan
func (a *AudioEngine) Encode(pcm []float32, inputChannelCount, inputSampleRate int) error {
	opusFrames, err := a.enc.Encode(pcm, inputChannelCount, inputSampleRate)
	if err != nil {
		internal.Logger.Error(err, "error encoding pcm")
	}

	go a.sendMedia(opusFrames)

	return nil
}

// sendMedia turns opus frames into media samples and sends them on the channel
func (a *AudioEngine) sendMedia(frames []internal.OpusFrame) {
	// REMOVE AFTER DEBUG
	internal.Logger.Info("DEBUG: Printing the media samples")
	for _, f := range frames {
		sample := convertOpusToSample(f)
		a.mediaOut <- sample
		// this is important to properly pace the samples
		time.Sleep(time.Millisecond * 20)
	}
	internal.Logger.Info("DEBUG: End of sendMedia")
}

func convertOpusToSample(frame internal.OpusFrame) media.Sample {
	return media.Sample{
		Data:               frame.Data,
		PrevDroppedPackets: 0, // FIXME support dropping packets
		Duration:           time.Millisecond * 20,
	}
}

// decode reads over the in channel in a loop, decodes the RTP packets to raw PCM and sends the data on another channel
func (a *AudioEngine) decode() {
	for {
		pkt, ok := <-a.rtpIn // pkt is the RTP packet received
		if !ok {
			internal.Logger.Info("rtpIn channel closed...")
			return
		}
		if !a.shouldInfer.Load() { // check if the "shouldInfer" var is true/false i.e., checking if we have paused/unpaused Ria listening
			continue
		}

		// // ** DEBUG:  **

		// // Marshal the RTP packet to a byte array

		// buf, _ := pkt.Marshal() // buf is a byte array

		// frtp, err := os.OpenFile("rtp_data.pcap",
		// 	os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) // options to append to the file, create file if doesn't exist and write only
		// if err != nil {
		// 	log.Println(err)
		// }
		// defer frtp.Close()

		// // writing the marshalled byte array to the pcap file
		// for _, value := range buf {
		// 	fmt.Fprintln(frtp, value) // print values to f, one per line
		// }

		// // ** END OF DEBUG **

		// // ** DEBUG **

		// frtp, err := os.OpenFile("rtp_data.ogg",
		// 	os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		// if err != nil {
		// 	log.Println(err)
		// }
		// defer frtp.Close()

		// oggFile, err := oggwriter.NewWith(frtp, 16000, 1)
		// if err != nil {
		// 	frtp.Close()
		// 	log.Println(err)
		// }
		// defer oggFile.Close()

		// // oggFile, err := oggwriter.New("output.ogg", 16000, 1)
		// // if err != nil {
		// // 	panic(err)
		// // }
		// // defer oggFile.Close()

		// if err := oggFile.WriteRTP(pkt); err != nil {
		// 	fmt.Println(err)
		// 	return
		// }

		// // ** END OF DEBUG **

		if a.firstTimeStamp == 0 {
			internal.Logger.Debug("Resetting timestamp bc firstTimeStamp is 0...  ", pkt.Timestamp)
			a.firstTimeStamp = pkt.Timestamp
		}
		// here the RTP packet is decoded (using VAD) into pcm and stored in a.pcm
		if _, err := a.decodePacket(pkt); err != nil {
			internal.Logger.Error(err, "error decoding opus packet ")
		}
		// ** DEBUG: the following is debug code to write the pcm data to a file **
		f, err := os.OpenFile("end_user_pcmdata.log",
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) // options to append to the file, create file if doesn't exist and write only
		if err != nil {
			log.Println(err)
		}
		defer f.Close()
		for _, value := range a.pcm {
			fmt.Fprintln(f, value) // print values to f, one per line
		}
		// ** END OF DEBUG **
	}
}

func (a *AudioEngine) decodePacket(pkt *rtp.Packet) (int, error) {
	_, err := a.dec.Decode(pkt.Payload, a.pcm)
	// we decode to float32 here since that is what whisper.cpp takes
	if err != nil {
		internal.Logger.Error(err, "error decoding fb packet")
		return 0, err
	} else {
		timestampMS := (pkt.Timestamp - a.firstTimeStamp) / ((sampleRate / 1000) * 3)
		lengthOfRecording := uint32(len(a.pcm) / (sampleRate / 1000))
		timestampRecordingEnds := timestampMS + lengthOfRecording
		a.sttEngine.Write(a.pcm, timestampRecordingEnds)
		return convertToBytes(a.pcm, a.buf), nil
	}
}

// This function converts f32le (PCM 32-bit floating-point little-endian) to s16le (PCM signed 16-bit little-endian) bytes for writing to a file
func convertToBytes(in []float32, out []byte) int {
	currIndex := 0
	for i := range in {
		res := int16(math.Floor(float64(in[i] * 32767)))

		out[currIndex] = byte(res & 0b11111111)
		currIndex++

		out[currIndex] = (byte(res >> 8))
		currIndex++
	}
	return currIndex
}
