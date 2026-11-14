package e2e

import (
	"bytes"
	"encoding/binary"
)

func silentWAV(samples int) []byte {
	const rate, bits = 8000, 16
	data := samples * bits / 8
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("RIFF")
	b.Write(le.AppendUint32(nil, uint32(36+data)))
	b.WriteString("WAVEfmt ")
	b.Write(le.AppendUint32(nil, 16))
	b.Write(le.AppendUint16(nil, 1))
	b.Write(le.AppendUint16(nil, 1))
	b.Write(le.AppendUint32(nil, rate))
	b.Write(le.AppendUint32(nil, rate*bits/8))
	b.Write(le.AppendUint16(nil, bits/8))
	b.Write(le.AppendUint16(nil, bits))
	b.WriteString("data")
	b.Write(le.AppendUint32(nil, uint32(data)))
	b.Write(make([]byte, data))
	return b.Bytes()
}
