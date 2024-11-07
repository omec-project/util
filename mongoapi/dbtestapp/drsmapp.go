// SPDX-FileCopyrightText: 2022-present Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0
//

package main

import (
	"os"
	"time"

	"github.com/omec-project/util/drsm"
	"github.com/omec-project/util/logger"
)

type drsmInterface struct {
	initDrsm bool
	Mode     drsm.DrsmMode
	d        *drsm.Drsm
	poolName string
}

var drsmIntf drsmInterface

func scanChunk(i int32) bool {
	logger.AppLog.Debugf("received callback from module to scan Chunk resource %+v", i)
	return false
}

func initDrsm(resName string) {
	if drsmIntf.initDrsm {
		return
	}
	drsmIntf.initDrsm = true
	drsmIntf.poolName = resName

	podn := os.Getenv("HOSTNAME") // pod-name
	podi := os.Getenv("POD_IP")
	podId := drsm.PodId{PodName: podn, PodIp: podi}
	db := drsm.DbInfo{Url: "mongodb://mongodb-arbiter-headless", Name: "sdcore"}

	t := time.Now().UnixNano()
	opt := &drsm.Options{}
	if t%2 == 0 {
		logger.AppLog.Debugln("running in Demux Mode")
		drsmIntf.Mode = drsm.ResourceDemux
	} else {
		opt.ResourceValidCb = scanChunk
		opt.IpPool = make(map[string]string)
		opt.IpPool["pool1"] = "192.168.1.0/24"
		opt.IpPool["pool2"] = "192.168.2.0/24"
	}
	drsmInitialize, _ := drsm.InitDRSM(resName, podId, db, opt)
	drsmIntf.d = drsmInitialize.(*drsm.Drsm)
}

func AllocateInt32One(resName string) int32 {
	id, err := drsmIntf.d.AllocateInt32ID()
	if err != nil {
		logger.AppLog.Debugf("id allocation error %+v", err)
		return 0
	}
	logger.AppLog.Infof("received id %d", id)
	return id
}

func AllocateInt32Many(resName string, number int32) []int32 {
	// code to acquire more than 1000 Ids
	var resIds []int32
	var count int32 = 0

	ticker := time.NewTicker(50 * time.Millisecond)
	for range ticker.C {
		id, _ := drsmIntf.d.AllocateInt32ID()
		if id != 0 {
			resIds = append(resIds, id)
		}
		logger.AppLog.Infof("received id %d", id)
		count++
		if count >= number {
			return resIds
		}
	}
	return resIds
}

func ReleaseInt32One(resName string, resId int32) error {
	err := drsmIntf.d.ReleaseInt32ID(resId)
	if err != nil {
		logger.AppLog.Debugf("id release error %+v", err)
		return err
	}
	return nil
}

func IpAddressAllocOne(pool string) (string, error) {
	ip, err := drsmIntf.d.AcquireIp(pool)
	if err != nil {
		logger.AppLog.Errorf("%+v: ip allocation error %+v", pool, err)
		return "", err
	}
	logger.AppLog.Infof("%v: received ip %v", pool, ip)
	return ip, nil
}

func IpAddressAllocMany(pool string, number int32) []string {
	var resIds []string
	var count int32 = 0

	ticker := time.NewTicker(50 * time.Millisecond)
	for range ticker.C {
		ip, err := drsmIntf.d.AcquireIp(pool)
		if err != nil {
			logger.AppLog.Errorf("%v: ip allocation error %v", pool, err)
		} else {
			logger.AppLog.Infof("%v: received ip %v", pool, ip)
			resIds = append(resIds, ip)
		}
		count++
		if count >= number {
			return resIds
		}
	}
	return resIds
}

func IpAddressRelease(pool, ip string) error {
	err := drsmIntf.d.ReleaseIp(pool, ip)
	return err
}
