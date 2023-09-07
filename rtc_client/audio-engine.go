package rtc_client

import (
	"bytes"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"time"

	stt "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/stt/engine"
	"github.com/infinityp913/rtc-go-server/rtc_client/internal"

	// stt "github.com/infinityp913/rtc-go-server/stt/engine"

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
// and to convert raw PCM audio from Coqui back to RTP Opus packets to be sent back over WebRTC
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
}

type FlaskResponse struct {
	New_state string    `json:"new_state"`
	Pcm_arr   []float32 `json:"response"`
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

	// // Looping through the chunks
	// for _, chunk := range chunked_pcm_arr {

	// 	Logger.Info("len(chunk): ", len(chunk))
	// 	Logger.Info("chunk: ", chunk) // REMOVE AFTER DEBUG

	// 	// pass it to ae.Encode(), where the pcm array is encoded to Opus frames AND
	// 	// they're sent over to the browser via WebRTC using the processOutgoingMedia() function in AudioEngine
	// 	s.ae.Encode(chunk, 1, 22050)
	// 	Logger.Info("After each encode") // REMOVE AFTER DEBUG

	// 	Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
	// 	go s.rtc.processOutgoingMedia()
	// }

	ae.Encode(pcm_arr, 1, 22050)

	Logger.Info("after encode") // REMOVE AFTER DEBUG

	// Logger.Info("calling go rtc.processOutgoingMedia within the loop") // REMOVE AFTER DEBUG
	// go s.rtc.processOutgoingMedia()

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
		internal.Logger.Info("DEBUG: Going to send sample to a.mediaOut")
		a.mediaOut <- sample
		internal.Logger.Info("DEBUG: Sent sample to a.mediaOut")
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
		pkt, ok := <-a.rtpIn
		if !ok {
			internal.Logger.Info("rtpIn channel closed...")
			return
		}
		if a.firstTimeStamp == 0 {
			internal.Logger.Debug("Resetting timestamp bc firstTimeStamp is 0...  ", pkt.Timestamp)
			a.firstTimeStamp = pkt.Timestamp
		}

		if _, err := a.decodePacket(pkt); err != nil {
			internal.Logger.Error(err, "error decoding opus packet ")
		}
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

// This function converts f32le to s16le bytes for writing to a file
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
