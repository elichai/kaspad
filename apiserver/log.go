package main

import (
	"github.com/daglabs/btcd/logger"
	"github.com/daglabs/btcd/util/panics"
)

var (
	log   = logger.BackendLog.Logger("APIS")
	spawn = panics.GoroutineWrapperFunc(log, logger.BackendLog)
)