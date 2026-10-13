package socksproxy

import (
	"bufio"
	"encoding/base64"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"io"
	"time"
)

func (x *session) run() {
	x.control.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(x.control)
	basic, ok := x.authenticate(reader)
	if !ok {
		return
	}
	h := make([]byte, 3)
	if _, e := io.ReadFull(reader, h); e != nil || h[0] != 5 || h[2] != 0 {
		return
	}
	target, e := readTarget(reader)
	if e != nil {
		writeReply(x.control, 8, zeroBind)
		return
	}
	x.control.SetDeadline(time.Time{})
	switch h[1] {
	case 1:
		if target.Port == 0 {
			writeReply(x.control, 1, zeroBind)
			return
		}
		x.connect(reader, target, basic)
	case 3:
		x.associate(reader, target)
	default:
		writeReply(x.control, 7, zeroBind)
	}
}
func (x *session) authenticate(r *bufio.Reader) (string, bool) {
	h := make([]byte, 2)
	if _, e := io.ReadFull(r, h); e != nil || h[0] != 5 || h[1] == 0 {
		return "", false
	}
	methods := make([]byte, int(h[1]))
	if _, e := io.ReadFull(r, methods); e != nil {
		return "", false
	}
	found := false
	for _, m := range methods {
		if m == 2 {
			found = true
		}
	}
	if !found {
		x.control.Write([]byte{5, 255})
		return "", false
	}
	if _, e := x.control.Write([]byte{5, 2}); e != nil {
		return "", false
	}
	if _, e := io.ReadFull(r, h); e != nil || h[0] != 1 {
		return "", false
	}
	user := make([]byte, int(h[1]))
	if _, e := io.ReadFull(r, user); e != nil {
		return "", false
	}
	n, e := r.ReadByte()
	if e != nil {
		return "", false
	}
	password := make([]byte, int(n))
	if _, e := io.ReadFull(r, password); e != nil {
		return "", false
	}
	defer clear(password)
	credential, e := captureadmission.NewProxyCredential(string(password))
	if e != nil || string(user) != captureadmission.ProxyUsername {
		x.control.Write([]byte{1, 1})
		return "", false
	}
	admission, e := x.server.options.Authorizer.Authorize(x.ctx, credential)
	if e != nil || admission.Validate() != nil {
		x.control.Write([]byte{1, 1})
		return "", false
	}
	ref, e := admission.CaptureReference()
	if e != nil {
		x.control.Write([]byte{1, 1})
		return "", false
	}
	x.mu.Lock()
	x.credential = credential
	x.admission = admission
	x.reference = ref
	x.mu.Unlock()
	if _, e := x.control.Write([]byte{1, 0}); e != nil {
		return "", false
	}
	basic := base64.StdEncoding.EncodeToString(append(append([]byte(captureadmission.ProxyUsername), ':'), password...))
	return basic, true
}
func (x *session) reauthorize() bool {
	a, e := x.server.options.Authorizer.Authorize(x.ctx, x.credential)
	if e != nil || a.Validate() != nil {
		x.close()
		return false
	}
	ref, e := a.CaptureReference()
	if e != nil || ref != x.reference || a.CredentialRevision() != x.admission.CredentialRevision() {
		x.close()
		return false
	}
	return true
}
