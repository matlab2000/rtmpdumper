package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"time"

	rtmp "github.com/zhangpeihao/gortmp"
)

const (
	programName = "RtmpPlayer"
	version     = "0.0.1"
)

var (
	rtmpurl   = flag.String("url", "", "The rtmp url to connect.")
	hevcid    = flag.Int("hevcid", 12, "HEVC codec type in flv tag")
	dumpVideo = flag.String("video", "test.264", "Dump 264/265 element stream into file.")
	dumpAudio = flag.String("audio", "", "Dump aac element stream into file.")
)

//OBController outbound controller
type OBController struct {
}

//AdtsInfo an adts header info,to construct adts header
//profileID profile identifier
//sampleRateIdx to index the sample rate
//chanNum channel number
type AdtsInfo struct {
	profileID     uint8
	sampleRateIdx uint8
	chanNum       uint8
}

/*
var sampleRates []byte = {96000,
    88200, 64000, 48000, 44100, 32000,24000,
    22050, 16000, 12000, 11025, 8000, 7350}
*/

//NewAdts from config to adts info
//config  the config bytes array contains the adts info
func NewAdts(config []byte) *AdtsInfo {
	c := config
	//objType := (c[0]>>3)&0xff  //5 Bit
	sampleIdx := (c[0]&0x7)<<1 | c[1]>>7 //4 Bit
	chanNum := (c[1] >> 3) & 0xf         //4 Bit
	return &AdtsInfo{profileID: 1, sampleRateIdx: sampleIdx,
		chanNum: chanNum}
}

//ToAdts fromo AdtsInfo to bytes
//size the aac frame length
func (info *AdtsInfo) ToAdts(size int) []byte {
	b := NewBStreamWriter(7)

	b.WriteBits(0xfff, 12)
	b.WriteBits(0, 1)
	b.WriteBits(0, 2)
	b.WriteBits(1, 1)
	b.WriteBits((uint64)(info.profileID), 2)
	b.WriteBits((uint64)(info.sampleRateIdx), 4)
	b.WriteBits(0, 1)
	b.WriteBits((uint64)(info.chanNum), 3)
	b.WriteBits(0, 1)
	b.WriteBits(0, 1)
	b.WriteBits(0, 1)
	b.WriteBits(0, 1)
	b.WriteBits((uint64)(size+7), 13)
	b.WriteBits(0x7ff, 11)
	b.WriteBits(0, 2)
	return b.Bytes()
}

var obConn rtmp.OutboundConn
var createStreamChan chan rtmp.OutboundStream
var videoDataSize int64
var audioDataSize int64
var videoFile *os.File
var audioFile *os.File
var status uint
var blen uint32

var fix = bytes.NewBuffer([]byte{0x0, 0x0, 0x0, 0x1}).Bytes()
var alen = []byte{0, 0, 0, 0}

var adts *AdtsInfo

//OnStatus rtmp status event
func (handler *OBController) OnStatus(conn rtmp.OutboundConn) {
	var err error
	status, err = conn.Status()
	fmt.Printf("status: %d, err: %v\n", status, err)
}

//OnClosed rtmp closed event
func (handler *OBController) OnClosed(conn rtmp.Conn) {
	fmt.Printf("Closed\n")
}

//AVCDecoderConfigurationRecord
/*
aligned(8) class AVCDecoderConfigurationRecord {
    ||0		unsigned int(8) configurationVersion = 1;
    ||1		unsigned int(8) AVCProfileIndication;
    ||2		unsigned int(8) profile_compatibility;
    ||3		unsigned int(8) AVCLevelIndication;
    ||4		bit(6) reserved = ‘111111’b;
            unsigned int(2) lengthSizeMinusOne; // offset 4
    ||5		bit(3) reserved = ‘111’b;
            unsigned int(5) numOfSequenceParameterSets;
    ||6		for (i = 0; i< numOfSequenceParameterSets; i++) {
                ||0	    unsigned int(16) sequenceParameterSetLength;
                ||2	    bit(8 * sequenceParameterSetLength) sequenceParameterSetNALUnit;
    }
    ||6+X	unsigned int(8) numOfPictureParameterSets;
            for (i = 0; i< numOfPictureParameterSets; i++) {
                ||0		unsigned int(16) pictureParameterSetLength;
                ||2		bit(8 * pictureParameterSetLength) pictureParameterSetNALUnit;
            }
}
*/

//HEVCDecoderConfigurationRecord
/*
aligned(8) class HEVCDecoderConfigurationRecord
{
    ||0		unsigned int(8) configurationVersion = 1;
    //vps[4]
    ||1		unsigned int(2) general_profile_space;
            unsigned int(1) general_tier_flag;
            unsigned int(5) general_profile_idc;
    //vps[5..8]
    ||2		unsigned int(32) general_profile_compatibility_flags;
    //
    ||6		unsigned int(48) general_constraint_indicator_flags;
    //vps[14]
    ||12	unsigned int(8) general_level_idc;
    ||13	bit(4) reserved = ‘1111’b;
            unsigned int(12) min_spatial_segmentation_idc;
    ||15	bit(6) reserved = ‘111111’b;
            unsigned int(2) parallelismType;
    ||16	bit(6) reserved = ‘111111’b;
            unsigned int(2) chroma_format_idc;
    ||17	bit(5) reserved = ‘11111’b;
            unsigned int(3) bit_depth_luma_minus8; //0
    ||18	bit(5) reserved = ‘11111’b;
            unsigned int(3) bit_depth_chroma_minus8; //0
    ||19	bit(16) avgFrameRate;
    ||21	bit(2) constantFrameRate;
            bit(3) numTemporalLayers;
            bit(1) temporalIdNested;
            unsigned int(2) lengthSizeMinusOne;
    ||22	unsigned int(8) numOfArrays;
    ||23	for (j=0; j < numOfArrays; j++)
            {
        ||0		bit(1) array_completeness;
                unsigned int(1) reserved = 0;
                unsigned int(6) NAL_unit_type;
        ||1		unsigned int(16) numNalus;
        ||3		for (i=0; i< numNalus; i++)
                {
                    unsigned int(16) nalUnitLength;
                    bit(8*nalUnitLength) nalUnit;
                }
            }
}
*/

//OnReceived rtmp received data callback
func (handler *OBController) OnReceived(conn rtmp.Conn, message *rtmp.Message) {
	switch message.Type {
	case rtmp.VIDEO_TYPE:
		if videoFile != nil {
			b := message.Buf.Bytes()
			blen = uint32(len(b))
			if b[1] == 0 {
				//decoder configuration record,key frame
				t := uint8(b[0]) & 0xf
				if t == 7 { //h264
					ioutil.WriteFile("avc.config.bin", b[5:], 0777)
					//------------------
					data := b[5:]
					profile := data[1]
					levelid := data[3]
					numSps := data[5] & 0x1f
					spsLen := (uint(data[6]))<<8 + uint(data[7])
					sps := data[8 : 8+spsLen]
					idx := 8 + spsLen
					numPps := data[idx]
					fmt.Printf("profile %d levelid %d numSps %d numPps %d\n", profile, levelid, numSps, numPps)
					idx++
					ppsLen := (uint(data[idx]))<<8 + uint(data[idx+1])
					idx += 2
					pps := data[idx : idx+ppsLen]
					//------------------
					videoFile.Write(fix)
					videoFile.Write(sps)
					videoFile.Write(fix)
					videoFile.Write(pps)

				} else if t == (uint8)(*hevcid) { //h265,this type value should be changed by rtmp server
					ioutil.WriteFile("hevc.config.bin", b[5:], 0777)
					//------------------
					vpsOK := false
					spsOK := false
					ppsOK := false

					data := b[5:]
					idx := 22
					numOfArrays := int(data[idx])
					idx++
					for i := 0; i < numOfArrays; i++ {
						nalUnitType := (data[idx]) & 0x3f
						idx++
						numNalus := int((uint(data[idx]))<<8 + uint(data[idx+1]))
						idx += 2
						for j := 0; j < numNalus; j++ {
							nalLen := int((uint(data[idx]))<<8 + uint(data[idx+1]))
							idx += 2
							body := data[idx : idx+nalLen]
							idx += nalLen
							if nalUnitType == 32 && vpsOK == false {
								videoFile.Write(fix)
								videoFile.Write(body)
								vpsOK = true
							}
							if nalUnitType == 33 && spsOK == false {
								videoFile.Write(fix)
								videoFile.Write(body)
								spsOK = true
							}
							if nalUnitType == 34 && ppsOK == false {
								videoFile.Write(fix)
								videoFile.Write(body)
								ppsOK = true
							}

						}

					}
					//------------------
				}
			} else if b[1] == 1 {
				videoFile.Write(fix)
				videoFile.Write(b[9:])
			}
		}
		videoDataSize += int64(message.Buf.Len())
	case rtmp.AUDIO_TYPE:
		if audioFile != nil {
			b := message.Buf.Bytes()
			if b[0] == 0xaf {
				if b[1] == 0x00 {
					adts = NewAdts(b[2:4])
					ioutil.WriteFile("config.bin", b, 0777)
				} else {
					data := b[2:]
					blen = uint32(len(data))
					hdr := adts.ToAdts(int(blen))
					audioFile.Write(hdr)
					audioFile.Write(data)
				}
			}

		}
		audioDataSize += int64(message.Buf.Len())
	}
}

//OnReceivedRtmpCommand rtmp received commond
func (handler *OBController) OnReceivedRtmpCommand(conn rtmp.Conn, command *rtmp.Command) {
	fmt.Printf("ReceviedCommand: %+v\n", command)
}

//OnStreamCreated rtmp stream create event
func (handler *OBController) OnStreamCreated(conn rtmp.OutboundConn, stream rtmp.OutboundStream) {
	fmt.Printf("Stream created: %d\n", stream.ID())
	createStreamChan <- stream
}

var url string
var stream string

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s version[%s]\r\nUsage: %s [OPTIONS]\r\n", programName, version, os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	index := strings.LastIndex(*rtmpurl, "/")
	s := *rtmpurl
	url = s[:index+1]
	stream = s[index+1:]
	fmt.Printf("rtmp:%s stream:%s video:%s audio:%s\r\n", url, stream, *dumpVideo, *dumpAudio)
	// Create raw video file
	if len(*dumpVideo) > 0 {
		var err error
		videoFile, err = os.Create(*dumpVideo)
		if err != nil {
			fmt.Println("Create video dump file error:", err)
			return
		}
	}
	// Create raw audio file
	if len(*dumpAudio) > 0 {
		var err error
		audioFile, err = os.Create(*dumpAudio)
		if err != nil {
			fmt.Println("Create audio dump file error:", err)
			return
		}
	}

	createStreamChan = make(chan rtmp.OutboundStream)
	testHandler := &OBController{}
	fmt.Println("to dial")

	var err error

	obConn, err = rtmp.Dial(url, testHandler, 100)
	if err != nil {
		fmt.Println("Dial error", err)
		os.Exit(-1)
	}

	defer obConn.Close()
	err = obConn.Connect()
	if err != nil {
		fmt.Printf("Connect error: %s", err.Error())
		os.Exit(-1)
	}
	for {
		select {
		case rtmpStream := <-createStreamChan:
			// Play
			err = rtmpStream.Play(stream, nil, nil, nil)
			if err != nil {
				fmt.Printf("Play error: %s", err.Error())
				os.Exit(-1)
			}

		case <-time.After(1 * time.Second):
			fmt.Printf("Audio size: %d bytes; Video size: %d bytes\n", audioDataSize, videoDataSize)
		}
	}
}

type Bit bool

const (
	zero Bit = false
	one  Bit = true
)

//BStream :
type BStream struct {
	stream      []byte
	remainCount uint8
}

//NewBStreamReader :
func NewBStreamReader(data []byte) *BStream {
	return &BStream{stream: data, remainCount: 8}
}

//NewBStreamWriter :
func NewBStreamWriter(nByte uint8) *BStream {
	return &BStream{stream: make([]byte, 0, nByte), remainCount: 0}
}

//WriteBit :
func (b *BStream) WriteBit(input Bit) {
	if b.remainCount == 0 {
		b.stream = append(b.stream, 0)
		b.remainCount = 8
	}

	latestIndex := len(b.stream) - 1
	if input {
		b.stream[latestIndex] |= 1 << (b.remainCount - 1)
	}
	b.remainCount--
}

//WriteOneByte :
func (b *BStream) WriteOneByte(data byte) {
	if b.remainCount == 0 {
		b.stream = append(b.stream, 0)
		b.remainCount = 8
	}

	latestIndex := len(b.stream) - 1

	b.stream[latestIndex] |= data >> (8 - b.remainCount)
	b.stream = append(b.stream, 0)
	latestIndex++
	b.stream[latestIndex] = data << b.remainCount
}

//WriteBits :
func (b *BStream) WriteBits(data uint64, count int) {
	data <<= uint(64 - count)

	//handle write byte if count over 8
	for count >= 8 {
		byt := byte(data >> (64 - 8))
		b.WriteOneByte(byt)

		data <<= 8
		count -= 8
	}

	//handle write Bit
	for count > 0 {
		bi := data >> (64 - 1)
		b.WriteBit(bi == 1)

		data <<= 1
		count--
	}
}

//ReadBit :
func (b *BStream) ReadBit() (Bit, error) {
	//empty return io.EOF
	if len(b.stream) == 0 {
		return zero, io.EOF
	}

	//if first byte already empty, move to next byte to retrieval
	if b.remainCount == 0 {
		b.stream = b.stream[1:]

		if len(b.stream) == 0 {
			return zero, io.EOF
		}

		b.remainCount = 8
	}

	// handle Bit retrieval
	retBit := b.stream[0] & 0x80
	b.stream[0] <<= 1
	b.remainCount--

	return retBit != 0, nil
}

//ReadByte :
func (b *BStream) ReadByte() (byte, error) {
	//empty return io.EOF
	if len(b.stream) == 0 {
		return 0, io.EOF
	}

	//if first byte already empty, move to next byte to retrieval
	if b.remainCount == 0 {
		b.stream = b.stream[1:]

		if len(b.stream) == 0 {
			return 0, io.EOF
		}

		b.remainCount = 8
	}

	//just remain 8 Bit, just return this byte directly
	if b.remainCount == 8 {
		byt := b.stream[0]
		b.stream = b.stream[1:]
		return byt, nil
	}

	//handle byte retrieval
	retByte := b.stream[0]
	b.stream = b.stream[1:]

	//check if we could finish retrieval on next byte
	if len(b.stream) == 0 {
		return 0, io.EOF
	}

	//handle remain Bit on next stream
	retByte |= b.stream[0] >> b.remainCount
	b.stream[0] <<= (8 - b.remainCount)
	return retByte, nil
}

//ReadBits :
func (b *BStream) ReadBits(count int) (uint64, error) {

	var retValue uint64

	//handle byte reading
	for count >= 8 {
		retValue <<= 8
		byt, err := b.ReadByte()
		if err != nil {
			return 0, err
		}
		retValue |= uint64(byt)
		count = count - 8
	}

	for count > 0 {
		retValue <<= 1
		bi, err := b.ReadBit()
		if err != nil {
			return 0, err
		}
		if bi {
			retValue |= 1
		}

		count--
	}

	return retValue, nil
}

// Bytes returns the bytes in the stream - taken from
// https://github.com/dgryski/go-tsz/bstream.go
func (b *BStream) Bytes() []byte {
	return b.stream
}
