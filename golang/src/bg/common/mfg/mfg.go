/*
 * COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

package mfg

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

var extSerialRE = regexp.MustCompile(`^(\d{3})-(\d{4})(\d{2})([A-Z]{2})-(\d{6})$`)

// ErrInvalidSerial represents an invalid serial number
var ErrInvalidSerial = errors.New("invalid serial number")

const minYear = 2018
const minSerial = 1
const maxSerial = 999899

// ExtSerial represents an externally visible (to customers) serial number, as
// defined by "External serial numbers"
// https://docs.google.com/document/d/1kEolvqtqjHVzdWlTv_SZ7lXpXF0do5P1HUa8BnS5vAw
type ExtSerial struct {
	Model    int
	Year     int
	Week     int
	SiteCode [2]byte
	Serial   int
}

func (s ExtSerial) String() string {
	return fmt.Sprintf("%03d-%04d%02d%c%c-%06d",
		s.Model,
		s.Year, s.Week, s.SiteCode[0], s.SiteCode[1],
		s.Serial)
}

// NewExtSerial creates an external serial number according to the
// input parameters.
func NewExtSerial(model, year, week int, siteCode [2]byte, serial int) (*ExtSerial, error) {
	if model < 1 || model > 999 {
		return nil, ErrInvalidSerial
	}
	if year < minYear || year > 9999 {
		return nil, ErrInvalidSerial
	}
	if week < 1 || week > 53 {
		return nil, ErrInvalidSerial
	}
	if siteCode[0] < 'A' || siteCode[0] > 'Z' {
		return nil, ErrInvalidSerial
	}
	if siteCode[1] < 'A' || siteCode[1] > 'Z' {
		return nil, ErrInvalidSerial
	}
	if serial < minSerial || serial > maxSerial {
		return nil, ErrInvalidSerial
	}
	return &ExtSerial{model, year, week, siteCode, serial}, nil
}

// NewExtSerialFromString parses a serial number from a string and returns
// a new ExtSerial
func NewExtSerialFromString(sn string) (*ExtSerial, error) {
	var err error
	match := extSerialRE.FindStringSubmatch(sn)
	if match == nil {
		return nil, ErrInvalidSerial
	}
	m, err := strconv.Atoi(match[1])
	if err != nil {
		return nil, ErrInvalidSerial
	}
	y, err := strconv.Atoi(match[2])
	if err != nil {
		return nil, ErrInvalidSerial
	}
	w, err := strconv.Atoi(match[3])
	if err != nil {
		return nil, ErrInvalidSerial
	}
	s, err := strconv.Atoi(match[5])
	if err != nil {
		return nil, ErrInvalidSerial
	}
	return NewExtSerial(m, y, w, [2]byte{match[4][0], match[4][1]}, s)
}