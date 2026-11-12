package disk

import "encoding/binary"

// parseLayout reads the GPT partition GUIDs from a DRIVE_LAYOUT_INFORMATION_EX.
func parseLayout(b []byte) [][16]byte {
	const (
		styleGPT    = 1
		header      = 48
		entry       = 144
		idOffset    = 48 // PARTITION_INFORMATION_EX.Gpt.PartitionId
		styleOffset = 0
	)
	le := binary.LittleEndian
	if len(b) < header || le.Uint32(b[0:]) != styleGPT {
		return nil
	}
	count := int(le.Uint32(b[4:]))
	var out [][16]byte
	for i := 0; i < count; i++ {
		e := b[header+i*entry:]
		if len(e) < entry {
			break
		}
		if le.Uint32(e[styleOffset:]) != styleGPT {
			continue
		}
		var g [16]byte
		copy(g[:], e[idOffset:idOffset+16])
		if g != ([16]byte{}) {
			out = append(out, g)
		}
	}
	return out
}

// PresentGUIDs returns the GPT partition GUIDs on all the disks; disks that
// can't be read are skipped.
func PresentGUIDs(disks []Disk) map[[16]byte]bool {
	out := map[[16]byte]bool{}
	for _, d := range disks {
		gs, err := PartitionGUIDs(d.Number)
		if err != nil {
			continue
		}
		for _, g := range gs {
			out[g] = true
		}
	}
	return out
}
