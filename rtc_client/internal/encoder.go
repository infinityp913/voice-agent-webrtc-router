package internal

import (
	"errors"
	"fmt"
	"sync"

	logr "github.com/GRVYDEV/S.A.T.U.R.D.A.Y/log"
	"github.com/GRVYDEV/S.A.T.U.R.D.A.Y/util"

	"gopkg.in/hraban/opus.v2"
)

const opusSampleRate = 48000

// PcmFrame is used for chunking raw pcm input into frames for the opus encoder
type PcmFrame struct {
	data  []float32
	index int
}

// OpusFrame contains and encoded opus frame
type OpusFrame struct {
	Data  []byte
	Index int
}

type OpusEncoder struct {
	enc         *opus.Encoder
	channels    int
	sampleRate  int
	frameSizeMs int
}

var Logger = logr.New()

func NewOpusEncoder(channels, frameSizeMs int) (*OpusEncoder, error) {
	if channels != 1 && channels != 2 {
		return nil, errors.New(fmt.Sprintf("invalid channel count expected 1 or 2 got %d", channels))
	}
	enc, err := opus.NewEncoder(opusSampleRate, channels, opus.AppRestrictedLowdelay)
	if err != nil {
		return nil, err
	}

	return &OpusEncoder{
		enc:         enc,
		channels:    channels,
		frameSizeMs: frameSizeMs,
	}, nil
}

// Encode will resample and encode the provided pcm audio to 48khz Opus
func (o *OpusEncoder) Encode(pcm []float32, inputChannelCount, inputSampleRate int) ([]OpusFrame, error) {
	if inputChannelCount != 1 && inputChannelCount != 2 {
		return []OpusFrame{}, errors.New(fmt.Sprintf("invalid inputChannelCount expected 1 or 2 got %d", inputChannelCount))
	}
	if inputChannelCount == 2 && o.channels == 1 {
		return []OpusFrame{}, errors.New("cannot currently downsample channels consider encoding to 2 channel")
	}
	if inputChannelCount == 1 && o.channels == 2 {
		pcm = util.ConvertToDualChannel(pcm)
	}
	if inputSampleRate != opusSampleRate {
		pcm = Resample(pcm, inputSampleRate, opusSampleRate)
	}
	frames := o.chunkPcm(pcm, opusSampleRate)

	// opusFrames := make([]OpusFrame, 0, len(frames))

	// for _, frame := range frames {
	// 	opusFrame, err := o.encodeToOpus(frame)
	// 	if err != nil {
	// 		Logger.Error(err, "error encoding opus frame")
	// 		return opusFrames, err
	// 	}

	// 	opusFrames = append(opusFrames, opusFrame)
	// }
	opusFrames := make([]OpusFrame, len(frames)) // made the opusFrames a slice of fixed length and capacity, cap=len to enable indexing below
	var wg sync.WaitGroup                        // the wait group makes sure that the main goroutine waits for all the spawned goroutines to finish before continuing, preventing the program from exiting prematurely.
	// var mu sync.Mutex                            //to ensure that access to the opusFrames slice (liek by audio-engine's sendMedia()) is serialized, preventing race conditions and potential data corruption.
	for idx, frame := range frames {
		wg.Add(1)
		frame := frame
		idx := idx
		// opusFrame, err := o.encodeToOpus(frame)
		func(idx_ int, frame_ PcmFrame) {
			defer wg.Done()
			opusFrame, err := o.encodeToOpus(frame_)
			if err != nil {
				Logger.Error(err, "$$$$$$$$$ ERROR IN o.encodeToOpus $$$$$$$$$$$$$$") // RISK: WE'RE NOT RETURNING THE ERROR OVER HERE
				return
			}
			// Use a mutex to synchronize access to opusFrames.
			// mu.Lock()
			opusFrames[idx_] = opusFrame // Since all goroutines write to different memory locations (coz of indexing) this isn't racy. [inspiration: https://stackoverflow.com/questions/18499352/golang-concurrency-how-to-append-to-the-same-slice-from-different-goroutines]
			// mu.Unlock()
		}(idx, frame)
	}
	wg.Wait()

	Logger.Infof("encoded %d opus frames", len(opusFrames))

	return opusFrames, nil

}

func (o *OpusEncoder) encodeToOpus(frame PcmFrame) (OpusFrame, error) {
	opusFrame := OpusFrame{Index: frame.index}
	data := make([]byte, 1000)

	n, err := o.enc.EncodeFloat32(frame.data, data)
	if err != nil {
		Logger.Errorf(err, "error encoding frame %+v", err)
		return opusFrame, err
	}
	opusFrame.Data = data[:n]

	return opusFrame, nil
}

// chunkPcm will split the provided pcm audio into properly sized frames
func (o *OpusEncoder) chunkPcm(pcm []float32, sampleRate int) []PcmFrame {
	// the amount of samples that fit into a frame
	outputFrameSize := o.channels * o.frameSizeMs * sampleRate / 1000
	// TODO make sure this rounds up
	totalFrames := len(pcm) / outputFrameSize

	frames := make([]PcmFrame, 0, totalFrames)

	idx := 0
	for idx <= totalFrames {
		pcmLen := len(pcm)
		// we have at least a full frame left
		if pcmLen > outputFrameSize {
			Logger.Debug("Got a full frame")
			frames = append(frames, PcmFrame{index: idx, data: pcm[:outputFrameSize]})
			// chop frame off of input
			pcm = pcm[outputFrameSize:]
			idx++
		} else {
			// we have less than a full frame so lets pad with silence
			sampleDelta := outputFrameSize - pcmLen
			silence := make([]float32, sampleDelta)

			Logger.Debugf("Got a partial frame len %d padding with %d silence samples", pcmLen, len(silence))

			frames = append(frames, PcmFrame{index: idx, data: append(pcm, silence...)})
			break
		}
	}

	Logger.Debugf("got %d frames", len(frames))

	return frames
}
