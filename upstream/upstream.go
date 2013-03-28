package upstream

import (
	"bytes"
	"github.com/fitstar/falcore"
	"io"
	"net"
	"net/http"
	"time"
)

type passThruReadCloser struct {
	io.Reader
	io.Closer
}

type Upstream struct {
	Transport *UpstreamTransport
	// Will ignore https on the incoming request and always upstream http
	ForceHttp bool
	// Ping URL Path-only for checking upness
	PingPath string
}

func NewUpstream(transport *UpstreamTransport) *Upstream {
	u := new(Upstream)
	u.Transport = transport
	return u
}

func (u *Upstream) FilterRequest(request *falcore.Request) (res *http.Response) {
	var err error
	req := request.HttpRequest

	// Force the upstream to use http 
	if u.ForceHttp || req.URL.Scheme == "" {
		req.URL.Scheme = "http"
		req.URL.Host = req.Host
	}
	before := time.Now()
	req.Header.Set("Connection", "Keep-Alive")
	var upstrRes *http.Response
	upstrRes, err = u.Transport.transport.RoundTrip(req)
	diff := falcore.TimeDiff(before, time.Now())
	if err == nil {
		// Copy response over to new record.  Remove connection noise.  Add some sanity.
		res = falcore.StringResponse(req, upstrRes.StatusCode, nil, "")
		if upstrRes.ContentLength > 0 {
			res.ContentLength = upstrRes.ContentLength
			res.Body = upstrRes.Body
		} else if res.ContentLength == -1 {
			res.Body = upstrRes.Body
			res.ContentLength = -1
			res.TransferEncoding = []string{"chunked"}
		} else {
			// Any bytes?
			var testBuf [1]byte
			n, _ := io.ReadFull(upstrRes.Body, testBuf[:])
			if n == 1 {
				// Yes there are.  Chunked it is.
				res.TransferEncoding = []string{"chunked"}
				res.ContentLength = -1
				rc := &passThruReadCloser{
					io.MultiReader(bytes.NewBuffer(testBuf[:]), upstrRes.Body),
					upstrRes.Body,
				}

				res.Body = rc
			} else {
				// There was an error reading the body
				upstrRes.Body.Close()
				res.ContentLength = 0
				res.Body = nil
			}
		}
		// Copy over headers with a few exceptions
		res.Header = make(http.Header)
		for hn, hv := range upstrRes.Header {
			switch hn {
			case "Content-Length":
			case "Connection":
			case "Transfer-Encoding":
			default:
				res.Header[hn] = hv
			}
		}
	} else {
		if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			falcore.Error("%s Upstream Timeout error: %v", request.ID, err)
			res = falcore.StringResponse(req, 504, nil, "Gateway Timeout\n")
			request.CurrentStage.Status = 2 // Fail
		} else {
			falcore.Error("%s Upstream error: %v", request.ID, err)
			res = falcore.StringResponse(req, 502, nil, "Bad Gateway\n")
			request.CurrentStage.Status = 2 // Fail
		}
	}
	falcore.Debug("%s [%s] [%s] %s s=%d Time=%.4f", request.ID, req.Method, u.Transport.host, req.URL, res.StatusCode, diff)
	return
}

func (u *Upstream) ping() (up bool, ok bool) {
	if u.PingPath != "" {
		// the url must be syntactically valid for this to work but the host will be ignored because we
		// are overriding the connection always
		request, err := http.NewRequest("GET", "http://localhost"+u.PingPath, nil)
		request.Header.Set("Connection", "Keep-Alive") // not sure if this should be here for a ping
		if err != nil {
			falcore.Error("Bad Ping request: %v", err)
			return false, true
		}
		res, err := u.Transport.transport.RoundTrip(request)

		if err != nil {
			falcore.Error("Failed Ping to %v:%v: %v", u.Transport.host, u.Transport.port, err)
			return false, true
		} else {
			res.Body.Close()
		}
		if res.StatusCode == 200 {
			return true, true
		}
		falcore.Error("Failed Ping to %v:%v: %v", u.Transport.host, u.Transport.port, res.Status)
		// bad status
		return false, true
	}
	return false, false
}
