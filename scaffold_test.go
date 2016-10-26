package goscaffold

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Scaffold Tests", func() {
	It("Validate framework", func() {
		s := CreateHTTPScaffold()
		stopChan := make(chan error)
		err := s.Open()
		Expect(err).Should(Succeed())

		go func() {
			fmt.Fprintf(GinkgoWriter, "Gonna listen on %s\n", s.InsecureAddress())
			stopErr := s.Listen(&testHandler{})
			fmt.Fprintf(GinkgoWriter, "Done listening\n")
			stopChan <- stopErr
		}()

		Eventually(func() bool {
			return testGet(s, "")
		}, 5*time.Second).Should(BeTrue())
		resp, err := http.Get(fmt.Sprintf("http://%s", s.InsecureAddress()))
		Expect(err).Should(Succeed())
		resp.Body.Close()
		Expect(resp.StatusCode).Should(Equal(200))
		shutdownErr := errors.New("Validate")
		s.Shutdown(shutdownErr)
		Eventually(stopChan).Should(Receive(Equal(shutdownErr)))
	})

	It("Separate management port", func() {
		s := CreateHTTPScaffold()
		s.SetManagementPort(0)
		stopChan := make(chan error)
		err := s.Open()
		Expect(err).Should(Succeed())

		go func() {
			fmt.Fprintf(GinkgoWriter, "Gonna listen on %s and %s\n",
				s.InsecureAddress(), s.ManagementAddress())
			stopErr := s.Listen(&testHandler{})
			fmt.Fprintf(GinkgoWriter, "Done listening\n")
			stopChan <- stopErr
		}()

		// Just make sure that it's up
		Eventually(func() bool {
			return testGet(s, "")
		}, 5*time.Second).Should(BeTrue())
		resp, err := http.Get(fmt.Sprintf("http://%s", s.InsecureAddress()))
		Expect(err).Should(Succeed())
		resp.Body.Close()
		Expect(resp.StatusCode).Should(Equal(200))
		resp, err = http.Get(fmt.Sprintf("http://%s", s.ManagementAddress()))
		Expect(err).Should(Succeed())
		resp.Body.Close()
		Expect(resp.StatusCode).Should(Equal(404))
		shutdownErr := errors.New("Validate")
		s.Shutdown(shutdownErr)
		Eventually(stopChan).Should(Receive(Equal(shutdownErr)))
	})

	It("Shutdown", func() {
		s := CreateHTTPScaffold()
		s.SetHealthPath("/health")
		s.SetReadyPath("/ready")
		stopChan := make(chan bool)
		err := s.Open()
		Expect(err).Should(Succeed())

		go func() {
			s.Listen(&testHandler{})
			stopChan <- true
		}()

		go func() {
			code, _ := getText(fmt.Sprintf("http://%s?delay=1s", s.InsecureAddress()))
			Expect(code).Should(Equal(200))
		}()

		// Just make sure server is listening
		Eventually(func() bool {
			return testGet(s, "")
		}, 5*time.Second).Should(BeTrue())

		// Ensure that we are healthy and ready
		code, _ := getText(fmt.Sprintf("http://%s/health", s.InsecureAddress()))
		Expect(code).Should(Equal(200))
		code, _ = getText(fmt.Sprintf("http://%s/ready", s.InsecureAddress()))
		Expect(code).Should(Equal(200))

		// Previous call prevents server from exiting
		Consistently(stopChan, 250*time.Millisecond).ShouldNot(Receive())

		// Tell the server to try and exit
		s.Shutdown(errors.New("Stop"))

		// Should take one second -- in the meantime, calls should fail with 503,
		// health should be good, but ready should be bad
		code, _ = getText(fmt.Sprintf("http://%s", s.InsecureAddress()))
		Expect(code).Should(Equal(503))
		code, _ = getText(fmt.Sprintf("http://%s/ready", s.InsecureAddress()))
		Expect(code).Should(Equal(503))
		code, _ = getText(fmt.Sprintf("http://%s/health", s.InsecureAddress()))
		Expect(code).Should(Equal(200))

		// But in less than two seconds, server should be down
		Eventually(stopChan, 2*time.Second).Should(Receive(BeTrue()))
		// Calls should now fail
		Eventually(func() bool {
			return testGet(s, "")
		}, time.Second).Should(BeFalse())
	})

	It("Health Check Functions", func() {
		status := int32(OK)
		var healthErr = &atomic.Value{}

		statusFunc := func() (HealthStatus, error) {
			stat := HealthStatus(atomic.LoadInt32(&status))
			av := healthErr.Load()
			if av != nil {
				errPtr := av.(*error)
				return stat, *errPtr
			}
			return stat, nil
		}

		s := CreateHTTPScaffold()
		s.SetManagementPort(0)
		s.SetHealthPath("/health")
		s.SetReadyPath("/ready")
		s.SetHealthChecker(statusFunc)
		stopChan := make(chan error)
		err := s.Open()
		Expect(err).Should(Succeed())

		go func() {
			fmt.Fprintf(GinkgoWriter, "Gonna listen on %s and %s\n",
				s.InsecureAddress(), s.ManagementAddress())
			stopErr := s.Listen(&testHandler{})
			fmt.Fprintf(GinkgoWriter, "Done listening\n")
			stopChan <- stopErr
		}()

		// Just make sure that it's up
		Eventually(func() bool {
			return testGet(s, "")
		}, 5*time.Second).Should(BeTrue())

		// Health should be good
		code, _ := getText(fmt.Sprintf("http://%s/health", s.ManagementAddress()))
		Expect(code).Should(Equal(200))
		code, _ = getText(fmt.Sprintf("http://%s/ready", s.ManagementAddress()))
		Expect(code).Should(Equal(200))

		// Mark down to "unhealthy" state. Should be bad.
		atomic.StoreInt32(&status, int32(Failed))
		code, bod := getText(fmt.Sprintf("http://%s/health", s.ManagementAddress()))
		Expect(code).Should(Equal(503))
		Expect(bod).Should(Equal("Failed"))
		code, _ = getText(fmt.Sprintf("http://%s/ready", s.ManagementAddress()))
		Expect(code).Should(Equal(503))

		// Change to merely "not ready" state. Should be healthy but not ready.
		atomic.StoreInt32(&status, int32(NotReady))
		code, _ = getText(fmt.Sprintf("http://%s/health", s.ManagementAddress()))
		Expect(code).Should(Equal(200))
		code, bod = getText(fmt.Sprintf("http://%s/ready", s.ManagementAddress()))
		Expect(code).Should(Equal(503))
		Expect(bod).Should(Equal("NotReady"))

		// Customize the error message.
		customErr := errors.New("Custom")
		healthErr.Store(&customErr)
		code, bod = getText(fmt.Sprintf("http://%s/ready", s.ManagementAddress()))
		Expect(code).Should(Equal(503))
		Expect(bod).Should(Equal("Custom"))

		// Check it in JSON
		code, js := getJSON(fmt.Sprintf("http://%s/ready", s.ManagementAddress()))
		Expect(code).Should(Equal(503))
		Expect(js["status"]).Should(Equal("NotReady"))
		Expect(js["reason"]).Should(Equal("Custom"))

		// Mark back up. Should be all good
		atomic.StoreInt32(&status, int32(OK))
		code, _ = getText(fmt.Sprintf("http://%s/health", s.ManagementAddress()))
		Expect(code).Should(Equal(200))
		code, _ = getText(fmt.Sprintf("http://%s/ready", s.ManagementAddress()))
		Expect(code).Should(Equal(200))

		s.Shutdown(nil)
		Eventually(stopChan).Should(Receive(Equal(ErrManualStop)))
	})
})

func getText(url string) (int, string) {
	req, err := http.NewRequest("GET", url, nil)
	Expect(err).Should(Succeed())
	req.Header.Set("Accept", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	Expect(err).Should(Succeed())
	defer resp.Body.Close()
	bod, err := ioutil.ReadAll(resp.Body)
	Expect(err).Should(Succeed())
	return resp.StatusCode, string(bod)
}

func getJSON(url string) (int, map[string]string) {
	req, err := http.NewRequest("GET", url, nil)
	Expect(err).Should(Succeed())
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	Expect(err).Should(Succeed())
	defer resp.Body.Close()
	bod, err := ioutil.ReadAll(resp.Body)
	Expect(err).Should(Succeed())
	var vals map[string]string
	err = json.Unmarshal(bod, &vals)
	Expect(err).Should(Succeed())
	return resp.StatusCode, vals
}

func testGet(s *HTTPScaffold, path string) bool {
	resp, err := http.Get(fmt.Sprintf("http://%s", s.InsecureAddress()))
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "Get %s = %s\n", path, err)
		return false
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintf(GinkgoWriter, "Get %s = %d\n", path, resp.StatusCode)
		return false
	}
	return true
}

type testHandler struct {
}

func (h *testHandler) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	var err error
	var delayTime time.Duration

	delayStr := req.URL.Query().Get("delay")
	if delayStr != "" {
		delayTime, err = time.ParseDuration(delayStr)
		if err != nil {
			resp.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	if delayTime > 0 {
		time.Sleep(delayTime)
	}
}
