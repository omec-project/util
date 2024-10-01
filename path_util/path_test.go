// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package path_util

import (
	"testing"

	"github.com/omec-project/util/logger"
)

func TestFree5gcPath(t *testing.T) {
	logger.PathLog.Infoln(Free5gcPath("free5gc/abcdef/abcdef.pem"))
}
