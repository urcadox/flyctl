// Package curl implements the curl command chain.
package curl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/internal/command"
	"github.com/superfly/flyctl/internal/config"
	"github.com/superfly/flyctl/internal/flag"
	"github.com/superfly/flyctl/internal/logger"
	"github.com/superfly/flyctl/internal/render"
	"github.com/superfly/flyctl/iostreams"
)

// New initializes and returns a new apps Command.
func New() (cmd *cobra.Command) {
	const (
		short = "Run a performance test against a URL"
		long  = short + "\n"
	)

	cmd = command.New("curl <URL>", short, long, run,
		command.RequireSession,
	)

	cmd.Args = cobra.ExactArgs(1)

	return
}

var Regions []string = []string{
	"aws:af-south-1",
	"aws:ap-east-1",
	"aws:ap-northeast-1",
	"aws:ap-northeast-2",
	"aws:ap-northeast-3",
	"aws:ap-south-1",
	"aws:ap-southeast-1",
	"aws:ap-southeast-2",
	"aws:ap-southeast-3",
	"aws:ca-central-1",
	"aws:eu-central-1",
	"aws:eu-north-1",
	"aws:eu-south-1",
	"aws:eu-west-1",
	"aws:eu-west-2",
	"aws:eu-west-3",
	"aws:me-south-1",
	"aws:sa-east-1",
	"aws:us-east-1",
	"aws:us-east-2",
	"aws:us-west-1",
	"aws:us-west-2",
	"fly:ams",
	"fly:fra",
}

func run(ctx context.Context) error {
	url, err := url.Parse(flag.FirstArg(ctx))
	if err != nil {
		return fmt.Errorf("invalid URL specified: %w", err)
	}

	// regionCodes, err := fetchRegionCodes(ctx)
	// if err != nil {
	// 	return err
	// }

	response, err := fetchTimings(ctx, url)
	if err != nil {
		return err
	}

	if io := iostreams.FromContext(ctx); !config.FromContext(ctx).JSONOutput {
		renderTextTimings(io.Out, io.ColorScheme(), response.Data.Regions)
	}

	fmt.Printf("%s\n", response.Data.Link)
	fmt.Println()
	return nil
}

type PlanetfallPayload struct {
	Method  string   `json:"method"`
	Url     string   `json:"url"`
	Regions []string `json:"regionIds"`
	Repeat  bool     `json:"repeat"`
}

func fetchTimings(ctx context.Context, url *url.URL) (resp *Response, err error) {

	payload := PlanetfallPayload{
		Url:     url.String(),
		Regions: Regions,
		Method:  "GET",
	}

	var buf bytes.Buffer
	if err = json.NewEncoder(&buf).Encode(payload); err != nil {
		return
	}

	var r *http.Request
	if r, err = http.NewRequestWithContext(ctx, http.MethodPost, "https://api.planetfall.io/v1/fly/curl", &buf); err != nil {
		return
	}
	token := os.Getenv("PLANETFALL_TOKEN")

	if token == "" {
		return nil, errors.New("Please set PLANETFALL_TOKEN")
	}
	r.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	r.Header.Add("Content-Type", "application/json")

	httpClient, err := api.NewHTTPClient(logger.FromContext(ctx), http.DefaultTransport)

	if err != nil {
		return
	}

	res, err := httpClient.Do(r)

	if err != nil {
		return
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		if body, err := io.ReadAll(res.Body); err == nil {
			err = errors.New(string(body))
			return nil, err
		}

		return
	}

	resp = &Response{}

	if err := json.NewDecoder(res.Body).Decode(resp); err != nil {
		err = fmt.Errorf("failed decoding response: %w", err)
		return nil, err
	}

	return
}

type Response struct {
	Data Data `json:"data"`
}
type Data struct {
	Link    string   `json:"link"`
	Regions []Region `json:"regions"`
}

type Region struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Checks []Check `json:"checks"`
}

type Check struct {
	ID      string `json:"id"`
	Latency int    `json:"latency"`
	Time    int    `json:"time"`
	Status  int    `json:"status"`
	Body    string `json:"body"`
	//headers: Record<string, string>, // just key values
	Timing Timing `json:"timing"`
}
type Timing struct {
	DnsStart          int `json:"dnsStart"`
	DnsDone           int `json:"dnsDone"`
	ConnectStart      int `json:"connectStart"`
	ConnectDone       int `json:"connectDone"`
	FirstByteStart    int `json:"firstByteStart"`
	FirstByteDone     int `json:"firstByteDone"`
	TlsHandshakeStart int `json:"tlsHandshakeStart"`
	TlsHandshakeDone  int `json:"tlsHandshakeDone"`
	TransferStart     int `json:"transferStart"`
	TransferDone      int `json:"transferDone"`
}

func (c *Check) formatedHTTPCode(cs *iostreams.ColorScheme) string {
	text := strconv.Itoa(c.Status)
	return colorize(cs, text, c.Status, 299, 399)
}

func (t *Timing) formattedDNS() string {
	dnsTiming := strconv.Itoa(t.DnsDone - t.DnsStart)
	return dnsTiming + "ms"
}

func (t *Timing) formattedConnect(cs *iostreams.ColorScheme) string {
	connectTiming := t.DnsDone - t.DnsStart
	text := strconv.Itoa(connectTiming) + "ms"
	return colorize(cs, text, connectTiming, 200, 500)
}

func (t *Timing) formattedTLS() string {
	return strconv.Itoa(t.TlsHandshakeDone-t.TlsHandshakeStart) + "ms"
}

func (t *Timing) formattedTTFB(cs *iostreams.ColorScheme) string {
	timing := t.FirstByteStart - t.DnsStart
	text := strconv.Itoa(timing) + "ms"
	return colorize(cs, text, timing, 400, 1000)
}

func colorize(cs *iostreams.ColorScheme, text string, val, greenCutoff, yellowCutoff int) string {
	var fn func(string) string
	switch {
	case val <= greenCutoff:
		fn = cs.Green
	case val <= yellowCutoff:
		fn = cs.Yellow
	default:
		fn = cs.Red
	}

	return fn(text)
}

func renderTextTimings(w io.Writer, cs *iostreams.ColorScheme, regions []Region) {
	var rows [][]string
	for _, region := range regions {

		coldCheck := region.Checks[0]

		rows = append(rows, []string{
			region.Name,
			coldCheck.formatedHTTPCode(cs),
			coldCheck.Timing.formattedDNS(),
			coldCheck.Timing.formattedConnect(cs),
			coldCheck.Timing.formattedTLS(),
			coldCheck.Timing.formattedTTFB(cs),
			strconv.Itoa(coldCheck.Latency) + "ms",
		})
	}

	render.Table(w, "", rows, "Region", "Status", "DNS", "Connect", "TLS", "TTFB", "Total")
}
