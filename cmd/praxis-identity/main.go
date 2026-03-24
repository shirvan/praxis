package main

import (
	"context"
	"log/slog"
	"os"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/config"
	"github.com/shirvan/praxis/internal/drivers/iamgroup"
	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	"github.com/shirvan/praxis/internal/drivers/iampolicy"
	"github.com/shirvan/praxis/internal/drivers/iamrole"
	"github.com/shirvan/praxis/internal/drivers/iamuser"
)

func main() {
	cfg := config.Load()
	auth := authservice.NewAuthClient()

	srv := server.NewRestate().
		Bind(restate.Reflect(iamrole.NewIAMRoleDriver(auth))).
		Bind(restate.Reflect(iampolicy.NewIAMPolicyDriver(auth))).
		Bind(restate.Reflect(iamuser.NewIAMUserDriver(auth))).
		Bind(restate.Reflect(iamgroup.NewIAMGroupDriver(auth))).
		Bind(restate.Reflect(iaminstanceprofile.NewIAMInstanceProfileDriver(auth)))

	slog.Info("starting identity driver pack", "addr", cfg.ListenAddr)
	if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
		slog.Error("identity driver pack exited", "err", err.Error())
		os.Exit(1)
	}
}
