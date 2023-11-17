package pbaleo

import (
	"time"
)

func (b *Block) ID() string {
	return b.BlockHash
}

func (b *Block) Number() uint64 {
	return uint64(b.Header.Metadata.Height)
}

func (b *Block) PreviousID() string {
	return b.PreviousHash
}

func (b *Block) Time() time.Time {
	return time.Unix(0, int64(b.Header.Metadata.Timestamp)).UTC()
}
