package cluster

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
)

type remoteReader struct {
	ctx       context.Context
	cancel    context.CancelFunc
	s         *Store
	key       string
	size, pos int64
	body      io.ReadCloser
	closed    bool
}

func (r *remoteReader) open() error {
	if e := r.ctx.Err(); e != nil {
		return e
	}
	if r.closed {
		return errors.New("download closed")
	}
	input := &s3.GetObjectInput{Bucket: aws.String(r.s.config.Bucket), Key: aws.String(r.key)}
	if r.pos > 0 {
		input.Range = aws.String(fmt.Sprintf("bytes=%d-", r.pos))
	}
	out, e := r.s.S3.GetObject(r.ctx, input)
	if e != nil {
		return e
	}
	if out.ContentLength == nil || *out.ContentLength != r.size-r.pos {
		out.Body.Close()
		return errors.New("stored blob length mismatch")
	}
	r.body = out.Body
	return nil
}
func (r *remoteReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	if r.closed {
		return 0, errors.New("download closed")
	}
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		if e := r.open(); e != nil {
			return 0, e
		}
	}
	n, e := r.body.Read(p)
	r.pos += int64(n)
	if e == io.EOF && r.pos < r.size {
		e = io.ErrUnexpectedEOF
	}
	return n, e
}
func (r *remoteReader) Seek(offset int64, whence int) (int64, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	if r.closed {
		return 0, errors.New("download closed")
	}
	next := offset
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		next += r.pos
	case io.SeekEnd:
		next += r.size
	default:
		return 0, errors.New("invalid seek")
	}
	if next < 0 || next > r.size {
		return 0, errors.New("seek outside object")
	}
	if next != r.pos {
		if r.body != nil {
			r.body.Close()
			r.body = nil
		}
		r.pos = next
	}
	return next, nil
}
func (r *remoteReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.cancel()
	if r.body != nil {
		return r.body.Close()
	}
	return nil
}
