// This code is an adapted version of Nats benchmarking suite from
// https://github.com/nats-io/go-nats/blob/master/examples/nats-bench.go
// for Centrifuge. Use together with benchmark program from Centrifuge repo:
// https://github.com/centrifugal/centrifuge/blob/master/examples/benchmark/main.go
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge-go"
)

// Some sane defaults.
const (
	DefaultNumMsg      = 100000
	DefaultNumPubs     = 1
	DefaultNumSubs     = 0
	DefaultMessageSize = 128
)

func usage() {
	log.Fatalf("Usage: benchmark [-s uri] [-np NUM_PUBLISHERS] [-ns NUM_SUBSCRIBERS] [-n NUM_MSGS] [-ms MESSAGE_SIZE] <channel>\n")
}

var url = flag.String("s", "ws://localhost:8000/connection/websocket", "Connection URI")
var numPubs = flag.Int("np", DefaultNumPubs, "Number of Concurrent Publishers")
var numSubs = flag.Int("ns", DefaultNumSubs, "Number of Concurrent Subscribers")
var numMsg = flag.Int("n", DefaultNumMsg, "Number of Messages to Publish")
var msgSize = flag.Int("ms", DefaultMessageSize, "Size of the message")

var benchmark *Benchmark

func main() {

	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		usage()
	}

	if *numMsg <= 0 {
		log.Fatal("Number of messages should be greater than zero.")
	}

	benchmark = NewBenchmark("Centrifuge", *numSubs, *numPubs)

	var startWg sync.WaitGroup
	var doneWg sync.WaitGroup

	doneWg.Add(*numPubs + *numSubs)

	// Run Subscribers first
	startWg.Add(*numSubs)
	for i := 0; i < *numSubs; i++ {
		go runSubscriber(&startWg, &doneWg, *numMsg, *msgSize)
	}
	startWg.Wait()

	// Now Publishers
	startWg.Add(*numPubs)
	pubCounts := MsgPerClient(*numMsg, *numPubs)
	for i := 0; i < *numPubs; i++ {
		go runPublisher(&startWg, &doneWg, pubCounts[i], *msgSize)
	}

	log.Printf("Starting benchmark [msgs=%d, msgsize=%d, pubs=%d, subs=%d]\n", *numMsg, *msgSize, *numPubs, *numSubs)

	startWg.Wait()
	doneWg.Wait()

	benchmark.Close()

	fmt.Print(benchmark.Report())
}

func newConnection() *centrifuge.Client {
	c := centrifuge.New(*url, centrifuge.DefaultConfig())

	events := &eventHandler{}
	c.OnError(events)
	c.OnDisconnect(events)
	return c
}

type eventHandler struct{}

func (h *eventHandler) OnError(_ *centrifuge.Client, _ centrifuge.ErrorEvent) {}

func (h *eventHandler) OnDisconnect(_ *centrifuge.Client, e centrifuge.DisconnectEvent) {
	if e.Reason != "clean disconnect" {
		log.Printf("disconnect: %s", e.Reason)
	}
}

func runPublisher(startWg, doneWg *sync.WaitGroup, numMsg int, msgSize int) {
	c := newConnection()
	defer func() { _ = c.Close() }()

	args := flag.Args()
	subj := args[0]
	var msg []byte
	if msgSize > 0 {
		msg = make([]byte, msgSize)
	}

	payload, _ := json.Marshal(string(msg))

	start := time.Now()

	err := c.Connect()
	if err != nil {
		log.Fatalf("Can't connect: %v\n", err)
	}

	startWg.Done()

	for i := 0; i < numMsg; i++ {
		_, err := c.Publish(subj, payload)
		if err != nil {
			log.Fatalf("error publish: %v", err)
		}
	}

	benchmark.AddPubSample(NewSample(numMsg, msgSize, start, time.Now()))
	doneWg.Done()
}

type subEventHandler struct {
	numMsg   int
	msgSize  int
	received int
	doneWg   *sync.WaitGroup
	startWg  *sync.WaitGroup
	client   *centrifuge.Client
	start    time.Time
}

func (h *subEventHandler) OnPublish(_ *centrifuge.Subscription, _ centrifuge.PublishEvent) {
	h.received++
	if h.received >= h.numMsg {
		benchmark.AddSubSample(NewSample(h.numMsg, h.msgSize, h.start, time.Now()))
		h.doneWg.Done()
		_ = h.client.Close()
	}
}

func (h *subEventHandler) OnSubscribeSuccess(_ *centrifuge.Subscription, _ centrifuge.SubscribeSuccessEvent) {
	h.startWg.Done()
}

func (h *subEventHandler) OnSubscribeError(_ *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	log.Fatalf("subscribe error: %v", e.Error)
}

func runSubscriber(startWg, doneWg *sync.WaitGroup, numMsg int, msgSize int) {
	c := newConnection()

	args := flag.Args()
	subj := args[0]

	subEvents := &subEventHandler{
		numMsg:  numMsg,
		msgSize: msgSize,
		doneWg:  doneWg,
		startWg: startWg,
		client:  c,
		start:   time.Now(),
	}

	sub, err := c.NewSubscription(subj)
	if err != nil {
		log.Fatalln(err)
	}

	sub.OnPublish(subEvents)
	sub.OnSubscribeSuccess(subEvents)
	sub.OnSubscribeError(subEvents)

	err = c.Connect()
	if err != nil {
		log.Fatalf("Can't connect: %v\n", err)
	}

	_ = sub.Subscribe()
}

// A Sample for a particular client
type Sample struct {
	JobMsgCnt int
	MsgCnt    uint64
	MsgBytes  uint64
	IOBytes   uint64
	Start     time.Time
	End       time.Time
}

// SampleGroup for a number of samples, the group is a Sample itself agregating the values the Samples
type SampleGroup struct {
	Sample
	Samples []*Sample
}

// Benchmark to hold the various Samples organized by publishers and subscribers
type Benchmark struct {
	Sample
	Name       string
	RunID      string
	Pubs       *SampleGroup
	Subs       *SampleGroup
	subChannel chan *Sample
	pubChannel chan *Sample
}

// NewBenchmark initializes a Benchmark. After creating a bench call AddSubSample/AddPubSample.
// When done collecting samples, call EndBenchmark
func NewBenchmark(name string, subCnt, pubCnt int) *Benchmark {
	bm := Benchmark{Name: name, RunID: strconv.Itoa(int(time.Now().Unix()))}
	bm.Subs = NewSampleGroup()
	bm.Pubs = NewSampleGroup()
	bm.subChannel = make(chan *Sample, subCnt)
	bm.pubChannel = make(chan *Sample, pubCnt)
	return &bm
}

// Close organizes collected Samples and calculates aggregates. After Close(), no more samples can be added.
func (bm *Benchmark) Close() {
	close(bm.subChannel)
	close(bm.pubChannel)

	for s := range bm.subChannel {
		bm.Subs.AddSample(s)
	}
	for s := range bm.pubChannel {
		bm.Pubs.AddSample(s)
	}

	if bm.Subs.HasSamples() {
		bm.Start = bm.Subs.Start
		bm.End = bm.Subs.End
	} else {
		bm.Start = bm.Pubs.Start
		bm.End = bm.Pubs.End
	}

	if bm.Subs.HasSamples() && bm.Pubs.HasSamples() {
		if bm.Start.After(bm.Subs.Start) {
			bm.Start = bm.Subs.Start
		}
		if bm.Start.After(bm.Pubs.Start) {
			bm.Start = bm.Pubs.Start
		}

		if bm.End.Before(bm.Subs.End) {
			bm.End = bm.Subs.End
		}
		if bm.End.Before(bm.Pubs.End) {
			bm.End = bm.Pubs.End
		}
	}

	bm.MsgBytes = bm.Pubs.MsgBytes + bm.Subs.MsgBytes
	bm.IOBytes = bm.Pubs.IOBytes + bm.Subs.IOBytes
	bm.MsgCnt = bm.Pubs.MsgCnt + bm.Subs.MsgCnt
	bm.JobMsgCnt = bm.Pubs.JobMsgCnt + bm.Subs.JobMsgCnt
}

// AddSubSample to the benchmark
func (bm *Benchmark) AddSubSample(s *Sample) {
	bm.subChannel <- s
}

// AddPubSample to the benchmark
func (bm *Benchmark) AddPubSample(s *Sample) {
	bm.pubChannel <- s
}

// NewSample creates a new Sample initialized to the provided values.
func NewSample(jobCount int, msgSize int, start, end time.Time) *Sample {
	s := Sample{JobMsgCnt: jobCount, Start: start, End: end}
	s.MsgBytes = uint64(msgSize * jobCount)
	return &s
}

// Throughput of bytes per second.
func (s *Sample) Throughput() float64 {
	return float64(s.MsgBytes) / s.Duration().Seconds()
}

// Rate of messages in the job per second.
func (s *Sample) Rate() int64 {
	return int64(float64(s.JobMsgCnt) / s.Duration().Seconds())
}

func (s *Sample) String() string {
	rate := commaFormat(s.Rate())
	throughput := HumanBytes(s.Throughput(), false)
	return fmt.Sprintf("%s msgs/sec ~ %s/sec", rate, throughput)
}

// Duration that the sample was active.
func (s *Sample) Duration() time.Duration {
	return s.End.Sub(s.Start)
}

// Seconds that the sample or samples were active.
func (s *Sample) Seconds() float64 {
	return s.Duration().Seconds()
}

// NewSampleGroup initializer.
func NewSampleGroup() *SampleGroup {
	s := new(SampleGroup)
	s.Samples = make([]*Sample, 0)
	return s
}

// Statistics information of the sample group (min, average, max and standard deviation).
func (sg *SampleGroup) Statistics() string {
	return fmt.Sprintf("min %s | avg %s | max %s | stddev %s msgs", commaFormat(sg.MinRate()), commaFormat(sg.AvgRate()), commaFormat(sg.MaxRate()), commaFormat(int64(sg.StdDev())))
}

// MinRate returns the smallest message rate in the SampleGroup.
func (sg *SampleGroup) MinRate() int64 {
	m := int64(0)
	for i, s := range sg.Samples {
		if i == 0 {
			m = s.Rate()
		}
		m = min(m, s.Rate())
	}
	return m
}

// MaxRate returns the largest message rate in the SampleGroup.
func (sg *SampleGroup) MaxRate() int64 {
	m := int64(0)
	for i, s := range sg.Samples {
		if i == 0 {
			m = s.Rate()
		}
		m = max(m, s.Rate())
	}
	return m
}

// AvgRate returns the average of all the message rates in the SampleGroup.
func (sg *SampleGroup) AvgRate() int64 {
	sum := uint64(0)
	for _, s := range sg.Samples {
		sum += uint64(s.Rate())
	}
	return int64(sum / uint64(len(sg.Samples)))
}

// StdDev returns the standard deviation the message rates in the SampleGroup.
func (sg *SampleGroup) StdDev() float64 {
	avg := float64(sg.AvgRate())
	sum := float64(0)
	for _, c := range sg.Samples {
		sum += math.Pow(float64(c.Rate())-avg, 2)
	}
	variance := sum / float64(len(sg.Samples))
	return math.Sqrt(variance)
}

// AddSample adds a Sample to the SampleGroup. After adding a Sample it shouldn't be modified.
func (sg *SampleGroup) AddSample(e *Sample) {
	sg.Samples = append(sg.Samples, e)

	if len(sg.Samples) == 1 {
		sg.Start = e.Start
		sg.End = e.End
	}
	sg.IOBytes += e.IOBytes
	sg.JobMsgCnt += e.JobMsgCnt
	sg.MsgCnt += e.MsgCnt
	sg.MsgBytes += e.MsgBytes

	if e.Start.Before(sg.Start) {
		sg.Start = e.Start
	}

	if e.End.After(sg.End) {
		sg.End = e.End
	}
}

// HasSamples returns true if the group has samples.
func (sg *SampleGroup) HasSamples() bool {
	return len(sg.Samples) > 0
}

// Report returns a human readable report of the samples taken in the Benchmark.
func (bm *Benchmark) Report() string {
	var buffer bytes.Buffer

	indent := ""
	if !bm.Pubs.HasSamples() && !bm.Subs.HasSamples() {
		return "No publisher or subscribers. Nothing to report."
	}

	if bm.Pubs.HasSamples() && bm.Subs.HasSamples() {
		buffer.WriteString(fmt.Sprintf("%s Pub/Sub stats: %s\n", bm.Name, bm))
		indent += " "
	}
	if bm.Pubs.HasSamples() {
		buffer.WriteString(fmt.Sprintf("%sPub stats: %s\n", indent, bm.Pubs))
		if len(bm.Pubs.Samples) > 1 {
			for i, stat := range bm.Pubs.Samples {
				buffer.WriteString(fmt.Sprintf("%s [%d] %v (%d msgs)\n", indent, i+1, stat, stat.JobMsgCnt))
			}
			buffer.WriteString(fmt.Sprintf("%s %s\n", indent, bm.Pubs.Statistics()))
		}
	}

	if bm.Subs.HasSamples() {
		buffer.WriteString(fmt.Sprintf("%sSub stats: %s\n", indent, bm.Subs))
		if len(bm.Subs.Samples) > 1 {
			for i, stat := range bm.Subs.Samples {
				buffer.WriteString(fmt.Sprintf("%s [%d] %v (%d msgs)\n", indent, i+1, stat, stat.JobMsgCnt))
			}
			buffer.WriteString(fmt.Sprintf("%s %s\n", indent, bm.Subs.Statistics()))
		}
	}
	return buffer.String()
}

func commaFormat(n int64) string {
	in := strconv.FormatInt(n, 10)
	out := make([]byte, len(in)+(len(in)-2+int(in[0]/'0'))/3)
	if in[0] == '-' {
		in, out[0] = in[1:], '-'
	}
	for i, j, k := len(in)-1, len(out)-1, 0; ; i, j = i-1, j-1 {
		out[j] = in[i]
		if i == 0 {
			return string(out)
		}
		if k++; k == 3 {
			j, k = j-1, 0
			out[j] = ','
		}
	}
}

// HumanBytes formats bytes as a human readable string
func HumanBytes(bytes float64, si bool) string {
	var base = 1024
	pre := []string{"K", "M", "G", "T", "P", "E"}
	var post = "B"
	if si {
		base = 1000
		pre = []string{"k", "M", "G", "T", "P", "E"}
		post = "iB"
	}
	if bytes < float64(base) {
		return fmt.Sprintf("%.2f B", bytes)
	}
	exp := int(math.Log(bytes) / math.Log(float64(base)))
	index := exp - 1
	units := pre[index] + post
	return fmt.Sprintf("%.2f %s", bytes/math.Pow(float64(base), float64(exp)), units)
}

func min(x, y int64) int64 {
	if x < y {
		return x
	}
	return y
}

func max(x, y int64) int64 {
	if x > y {
		return x
	}
	return y
}

// MsgPerClient divides the number of messages by the number of clients and tries
// to distribute them as evenly as possible
func MsgPerClient(numMsg, numClients int) []int {
	var counts []int
	if numClients == 0 || numMsg == 0 {
		return counts
	}
	counts = make([]int, numClients)
	mc := numMsg / numClients
	for i := 0; i < numClients; i++ {
		counts[i] = mc
	}
	extra := numMsg % numClients
	for i := 0; i < extra; i++ {
		counts[i]++
	}
	return counts
}
