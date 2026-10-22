package runtimepersistence

import (
	"encoding/binary"
	"io"
	"os"
)

// SQLite may remove a genuine header-only WAL when its last read-write handle
// closes, even if it ran only query_only SQL. Snapshot proof of zero frames;
// never normalize malformed/truncated headers or any byte of frame data.
func validFrameFreeInspectionWAL(path string, database, wal os.FileInfo) (bool, error) {
	header, err := inspectionFilePrefix(path+"-wal", wal, 32)
	if err != nil {
		return false, err
	}
	magic := binary.BigEndian.Uint32(header[:4])
	pageSize := binary.BigEndian.Uint32(header[8:12])
	if magic&0xfffffffe != 0x377f0682 || binary.BigEndian.Uint32(header[4:8]) != 3007000 || pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return false, nil
	}
	var order binary.ByteOrder = binary.LittleEndian
	if magic&1 != 0 {
		order = binary.BigEndian
	}
	var s0, s1 uint32
	for offset := 0; offset < 24; offset += 8 {
		s0 += order.Uint32(header[offset:offset+4]) + s1
		s1 += order.Uint32(header[offset+4:offset+8]) + s0
	}
	if s0 != binary.BigEndian.Uint32(header[24:28]) || s1 != binary.BigEndian.Uint32(header[28:32]) {
		return false, nil
	}
	db, err := inspectionFilePrefix(path, database, 18)
	if err != nil {
		return false, err
	}
	dbPageSize := uint32(binary.BigEndian.Uint16(db[16:18]))
	if dbPageSize == 1 {
		dbPageSize = 65536
	}
	return pageSize == dbPageSize, nil
}

func inspectionFilePrefix(path string, expected os.FileInfo, count int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInspectionChanged
	}
	defer file.Close()
	stable := func(info os.FileInfo, err error) bool {
		return err == nil && info.Mode().IsRegular() && os.SameFile(expected, info) && expected.Size() == info.Size() && expected.ModTime().Equal(info.ModTime())
	}
	if !stable(file.Stat()) {
		return nil, ErrInspectionChanged
	}
	data := make([]byte, count)
	if _, err = io.ReadFull(file, data); err != nil {
		return nil, ErrInspectionChanged
	}
	if !stable(file.Stat()) || !stable(os.Lstat(path)) {
		return nil, ErrInspectionChanged
	}
	return data, nil
}
