package prefixedio

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Reader reads from another io.Reader and de-multiplexes line-oriented
// data into different io.Reader streams.
//
// Lines are delimited with the '\n' character.
//
// When `Read` is called, any data that doesn't currently have a prefix
// registered will be discarded. Data won't start being discarded until
// the first Read is called on a prefix. Once the first Read is called,
// data is read until EOF. Therefore, be sure to request all prefix
// readers before issuing any Read calls on any prefixes.
//
// Reads will block if all the readers aren't routinely draining their
// buffers. Therefore, be sure to be actively reading from all registered
// prefixes, otherwise you can encounter deadlock scenarios.
type Reader struct {
	done     bool
	prefixes map[string]*io.PipeWriter
	r        io.Reader

	l    sync.Mutex
	once sync.Once
}

// New creates a new Reader with the given io.Reader.
func New(r io.Reader) (*Reader, error) {
	if r == nil {
		return nil, errors.New("Reader must not be nil")
	}

	return &Reader{r: r}, nil
}

// Prefix returns a new io.Reader that will read data that
// is prefixed with the given prefix.
//
// The read data is line-oriented so calling Read will result
// in a full line of output (including the line separator),
// but is exposed as an io.Reader for useful utility interoperating
// with other Go libraries.
//
// The data read has the prefix stripped, but contains the line
// delimiter.
func (r *Reader) Prefix(p string) (io.Reader, error) {
	r.l.Lock()
	defer r.l.Unlock()

	if r.prefixes == nil {
		r.prefixes = make(map[string]*io.PipeWriter)
	}

	if _, ok := r.prefixes[p]; ok {
		fmt.Errorf("Prefix already registered: %s", p)
	}

	pr, pw := io.Pipe()
	r.prefixes[p] = pw

	if r.done {
		pw.Close()
	}

	return &prefixReader{
		r:  r,
		pr: pr,
	}, nil
}

// init starts the goroutine that reads from the underlying reader
// and sends data to the proper place.
//
// This is safe to call multiple times.
func (r *Reader) init() {
	r.once.Do(func() {
		go r.read()
	})
}

// read runs in a goroutine and performs a read on the reader,
// dispatching lines to prefixes where necessary.
func (r *Reader) read() {
	var err error
	buf := bufio.NewReader(r.r)

	for {
		var line []byte
		line, err = buf.ReadBytes('\n')

		// If an error occurred and its not an EOF, then report that
		// error to all pipes and exit.
		if err != nil && err != io.EOF {
			break
		}

		// Go through each prefix and write if the line matches.
		// If no lines match, the data is lost.
		r.l.Lock()
		for p, pw := range r.prefixes {
			if bytes.HasPrefix(line, []byte(p)) {
				data := line[len(p):]
				n := 0

				// Make sure we write all the data before we exit.
				for n < len(data) {
					ni, err := pw.Write(data[n:])
					if err != nil {
						break
					}

					n += ni
				}

				break
			}
		}
		r.l.Unlock()

		if err == io.EOF {
			break
		}
	}

	r.l.Lock()
	defer r.l.Unlock()

	// Mark us done so that we don't create anymore readers
	r.done = true

	// All previous writers should be closed so that the readers
	// properly return an EOF (or another error if we had one)
	for _, pw := range r.prefixes {
		if err != nil && err != io.EOF {
			pw.CloseWithError(err)
		} else {
			pw.Close()
		}
	}
}

type prefixReader struct {
	r  *Reader
	pr io.Reader
}

func (r *prefixReader) Read(p []byte) (int, error) {
	r.r.init()
	return r.pr.Read(p)
}
