package termimg

// limitGIF walks the block structure of a GIF without decoding pixel data
// and, if keeping every frame would exceed the frame or memory budget, returns
// a copy truncated before the first frame over budget (with a trailer
// appended). gif.DecodeAll keeps every frame in memory before compositeGIF
// can drop any, so a small file with many large, highly compressible frames
// would otherwise allocate gigabytes. Malformed data is returned unchanged
// for gif.DecodeAll to reject.
func limitGIF(data []byte, maxFrames int, maxBytes int64) []byte {
	if len(data) < 13 || string(data[:3]) != "GIF" {
		return data
	}
	p := 13
	if data[10]&0x80 != 0 {
		p += 3 << (int(data[10]&7) + 1)
	}
	skipSubBlocks := func(p int) (int, bool) {
		for p < len(data) {
			n := int(data[p])
			p++
			if n == 0 {
				return p, true
			}
			p += n
		}
		return p, false
	}
	frames := 0
	var total int64
	pending := -1 // start of extensions preceding the next image
	for p < len(data) {
		switch data[p] {
		case 0x21: // extension
			if pending < 0 {
				pending = p
			}
			if p+2 > len(data) {
				return data
			}
			var ok bool
			if p, ok = skipSubBlocks(p + 2); !ok {
				return data
			}
		case 0x2C: // image descriptor
			start := p
			if pending >= 0 {
				start = pending
			}
			pending = -1
			if p+10 > len(data) {
				return data
			}
			w := int64(data[p+5]) | int64(data[p+6])<<8
			h := int64(data[p+7]) | int64(data[p+8])<<8
			packed := data[p+9]
			p += 10
			if packed&0x80 != 0 {
				p += 3 << (int(packed&7) + 1)
			}
			frames++
			total += w * h
			if frames > 1 && (frames > maxFrames || total > maxBytes) {
				out := make([]byte, start+1)
				copy(out, data[:start])
				out[start] = 0x3B
				return out
			}
			p++ // LZW minimum code size
			var ok bool
			if p, ok = skipSubBlocks(p); !ok {
				return data
			}
		default: // trailer or garbage
			return data
		}
	}
	return data
}
