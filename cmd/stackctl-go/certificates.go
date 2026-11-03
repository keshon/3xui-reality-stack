package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/certificates"
)

const certificateUsage = "usage: stackctl-go export-certificates --source /absolute/acme.json --destination /absolute/certs --domain example.com [--resolver le] [--watch --interval 60s]"

func exportCertificates(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("export-certificates", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	options := certificates.Options{}
	f.StringVar(&options.Source, "source", "", "Traefik ACME JSON file")
	f.StringVar(&options.Destination, "destination", "", "existing certificate output directory")
	f.StringVar(&options.Resolver, "resolver", "le", "Traefik resolver name")
	f.StringVar(&options.Domain, "domain", "", "certificate filename basename")
	watch := f.Bool("watch", false, "poll ACME state until interrupted")
	interval := f.Duration("interval", time.Minute, "certificate polling interval")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return errors.New(certificateUsage)
	}
	exporter, err := certificates.New(options)
	if err != nil {
		return err
	}
	emit := func(report certificates.Report) error { return json.NewEncoder(out).Encode(report) }
	if *watch {
		return exporter.Watch(ctx, *interval, emit)
	}
	if *interval != time.Minute {
		return errors.New("certificate interval requires --watch")
	}
	report, err := exporter.Export(ctx)
	if err != nil {
		return err
	}
	return emit(report)
}
