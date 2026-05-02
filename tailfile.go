package supervisor

import (
	"context"
	"io"
	"os"
	"time"
)

// TailFile incrementally reads a growing file, calling onGrow for new
// bytes and onIdle when no growth for a while. Blocks until ctx is
// cancelled or an error occurs.
func TailFile(ctx context.Context, path string, interval time.Duration,
	onGrow func([]byte), onIdle func(time.Duration)) error {

	var offset int64
	lastGrowth := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			f, err := os.Open(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue // file not created yet
				}
				return err
			}

			info, err := f.Stat()
			if err != nil {
				f.Close()
				return err
			}

			size := info.Size()
			if size > offset {
				if _, err := f.Seek(offset, io.SeekStart); err != nil {
					f.Close()
					return err
				}
				buf := make([]byte, size-offset)
				n, err := io.ReadFull(f, buf)
				f.Close()
				if err != nil && err != io.ErrUnexpectedEOF {
					return err
				}
				if n > 0 {
					onGrow(buf[:n])
					offset += int64(n)
					lastGrowth = time.Now()
				}
			} else {
				f.Close()
				if onIdle != nil {
					idle := time.Since(lastGrowth)
					onIdle(idle)
				}
			}
		}
	}
}
