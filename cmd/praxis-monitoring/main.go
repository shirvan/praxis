package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/drivers/dashboard"
	"github.com/shirvan/praxis/internal/drivers/loggroup"
	"github.com/shirvan/praxis/internal/drivers/metricalarm"
)

func main() {
	cfg := config.Load()
	auth := authservice.NewAuthClient()
	rp := config.DefaultRetryPolicy()

	srv := server.NewRestate().
		Bind(restate.Reflect(loggroup.NewLogGroupDriver(auth), rp)).
		Bind(restate.Reflect(metricalarm.NewMetricAlarmDriver(auth), rp)).
		Bind(restate.Reflect(dashboard.NewDashboardDriver(auth), rp))

	slog.Info("starting monitoring driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("monitoring driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
