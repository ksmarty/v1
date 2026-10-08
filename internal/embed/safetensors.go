// safetensors.go reads the safetensors weight format.
//
// safetensors is an 8-byte little-endian header length, a JSON header naming
// every tensor with its dtype, shape and byte range, then one contiguous data
// section. It is deliberately trivial to parse — no Python, no ONNX runtime, no
// cgo — which is what makes native embedding possible inside a single Go binary
// that cross-compiles with CGO_ENABLED=0.
package embed

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// tensor is a decoded weight matrix or vector, always float32.
//
// Every published dtype (F32, F16, BF16) decodes to float32 so the compute path
// has a single numeric type. The published shapes are PyTorch's [out, in] for a
// linear layer, so a forward pass is y = W·x + b, not x·W.
type tensor struct {
	shape []int
	data  []float32
}

// rows and cols describe a 2-D tensor; a 1-D tensor reports rows 1.
func (t *tensor) rows() int { return t.shape[0] }
func (t *tensor) cols() int {
	if len(t.shape) < 2 {
		return 1
	}
	return t.shape[1]
}

type stEntry struct {
	DType  string `json:"dtype"`
	Shape  []int  `json:"shape"`
	Offset [2]int `json:"data_offsets"`
}

// readSafetensors decodes a .safetensors file into named float32 tensors.
//
// Tensors are read one at a time with ReadAt rather than by loading the file
// whole: a 137M-parameter model is a ~550 MB file, and reading it all would
// double peak memory for the copy alone.
func readSafetensors(path string) (map[string]*tensor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lenBuf [8]byte
	if _, err := f.ReadAt(lenBuf[:], 0); err != nil {
		return nil, fmt.Errorf("safetensors: read header length: %w", err)
	}
	headerLen := binary.LittleEndian.Uint64(lenBuf[:])
	if headerLen == 0 || headerLen > 1<<28 {
		return nil, fmt.Errorf("safetensors: implausible header length %d", headerLen)
	}
	header := make([]byte, headerLen)
	if _, err := f.ReadAt(header, 8); err != nil {
		return nil, fmt.Errorf("safetensors: read header: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(header, &raw); err != nil {
		return nil, fmt.Errorf("safetensors: parse header: %w", err)
	}

	// Tensor data offsets are relative to the end of the header, not the file.
	const dataStart = 8
	out := make(map[string]*tensor, len(raw))
	for name, body := range raw {
		if name == "__metadata__" {
			continue
		}
		var e stEntry
		if err := json.Unmarshal(body, &e); err != nil {
			return nil, fmt.Errorf("safetensors: parse %s: %w", name, err)
		}
		// Integer and boolean tensors are buffers, not weights: BERT exports
		// carry `embeddings.position_ids` as I64. The forward pass never reads
		// them, so they are skipped instead of failing the load.
		if !isFloatDType(e.DType) {
			continue
		}
		n := 1
		for _, d := range e.Shape {
			n *= d
		}
		size := e.Offset[1] - e.Offset[0]
		buf := make([]byte, size)
		if _, err := f.ReadAt(buf, int64(dataStart+headerLen+uint64(e.Offset[0]))); err != nil {
			return nil, fmt.Errorf("safetensors: read %s: %w", name, err)
		}
		data, err := decodeFloats(buf, e.DType)
		if err != nil {
			return nil, fmt.Errorf("safetensors: %s: %w", name, err)
		}
		if len(data) != n {
			return nil, fmt.Errorf("safetensors: %s: got %d values, shape %v wants %d", name, len(data), e.Shape, n)
		}
		out[name] = &tensor{shape: e.Shape, data: data}
	}
	return out, nil
}

// isFloatDType reports whether a dtype holds weights this package can decode.
// Anything else is a buffer the encoder does not read.
func isFloatDType(dtype string) bool {
	switch dtype {
	case "F32", "F16", "BF16":
		return true
	}
	return false
}

// decodeFloats converts a tensor's raw bytes to float32.
//
// BF16 and F16 are both published in the wild (F16 is the default of many HF
// exports, BF16 of most recent ones) and neither is a float32 with the low bits
// zeroed, so both are widened explicitly.
func decodeFloats(buf []byte, dtype string) ([]float32, error) {
	switch dtype {
	case "F32":
		if len(buf)%4 != 0 {
			return nil, fmt.Errorf("F32 payload of %d bytes is not a multiple of 4", len(buf))
		}
		out := make([]float32, len(buf)/4)
		for i := range out {
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
		}
		return out, nil
	case "F16":
		if len(buf)%2 != 0 {
			return nil, fmt.Errorf("F16 payload of %d bytes is not a multiple of 2", len(buf))
		}
		out := make([]float32, len(buf)/2)
		for i := range out {
			out[i] = halfToFloat(binary.LittleEndian.Uint16(buf[i*2:]))
		}
		return out, nil
	case "BF16":
		if len(buf)%2 != 0 {
			return nil, fmt.Errorf("BF16 payload of %d bytes is not a multiple of 2", len(buf))
		}
		out := make([]float32, len(buf)/2)
		for i := range out {
			// bfloat16 is the top 16 bits of a float32, so widening is a shift.
			out[i] = math.Float32frombits(uint32(binary.LittleEndian.Uint16(buf[i*2:])) << 16)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported dtype %q (expected F32, F16 or BF16)", dtype)
	}
}

// halfToFloat widens an IEEE 754 binary16 to float32, including subnormals,
// infinities and NaN. Half precision is common enough in HF exports that
// mishandling subnormals would show up as subtly wrong embeddings.
func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h & 0x03ff)
	switch exp {
	case 0:
		if frac == 0 {
			return math.Float32frombits(sign) // signed zero
		}
		// Subnormal: renormalise by shifting the fraction until the implicit
		// leading bit is found, adjusting the exponent as we go.
		e := uint32(127 - 15 + 1)
		for frac&0x0400 == 0 {
			frac <<= 1
			e--
		}
		frac &= 0x03ff
		return math.Float32frombits(sign | e<<23 | frac<<13)
	case 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | frac<<13) // inf or NaN
	default:
		return math.Float32frombits(sign | (exp-15+127)<<23 | frac<<13)
	}
}
