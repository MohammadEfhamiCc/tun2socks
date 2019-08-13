// +build stats

package main

import (
	"flag"

	"github.com/xjasonlyu/tun2socks/common/log"
	"github.com/xjasonlyu/tun2socks/common/stats/session"
)

func init() {
	args.Stats = flag.Bool("stats", false, "Enable statistics")
	args.StatsAddr = flag.String("statsAddr", "localhost:6001", "Listen address of stats, open in your browser to view statistics")

	addPostFlagsInitFn(func() {
		if *args.Stats {
			sessionStater = session.NewSimpleSessionStater()

			// stats variables
			session.StatsAddr = *args.StatsAddr
			session.StatsVersion = version

			// start session stater
			if err := sessionStater.Start(); err != nil {
				log.Fatalf("start session stater failed: %v", err)
			}
		} else {
			sessionStater = nil
		}
	})
}