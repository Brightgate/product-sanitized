//
// COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
//
// This copyright notice is Copyright Management Information under 17 USC 1202
// and is included to protect this work and deter copyright infringement.
// Removal or alteration of this Copyright Management Information without the
// express written permission of Brightgate Inc is prohibited, and any
// such unauthorized removal or alteration will be a violation of federal law.
//

// A Bayesian classifier is a supervised trained classifier.  With the
// accumulation of the training set, the classifier is capable of
// calculating the probability that a record matches a well-defined
// class in the training set.
//
// Training is dependent on the device and training tables in the
// primary database.

// Bayesian classifiers return the best matching class and probability.
// Three parameters are used to tune the behavior of a classifier, other
// than manipulating the membership of the training set:
//
// 1.  The _minimum class size_ represents the minimum number of entries
//     a class must have within the training set to be a valid result
//     returned in a classification.  Increasing the minimum class size
//     means that the training set must be large enough to contain at
//     least that many instances of each result.  Reducing the minimum
//     class size means that attributes shared among classes will
//     potentially be less well resolved.
//
// 2.  The _certain above_ parameter is a real-valued number
//     representing the probability above which we believe the
//     classification is meaningful.
//
// 3.  The _uncertain below_ parameter is a real-valued number
//     representing the probability below which we drop our belief that
//     a previously certain prediction is still meaningful.
//
// Because we are accumulating training data, we have kept our minimum
// class sizes small.  These should be increased gradually, as
// additional data is acquired; the trade-off is that a larger number of
// instances becomes required to add a new result to the classifier.
//
// The certain-uncertain parameters have typically been chosen such that
// the result is at least twice any next possible result to be certain,
// and that the result is less than 50-50 to lose that certainty.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lytics/multibayes"
	"github.com/pkg/errors"
)

// A machine is a identifiable target that has a MAC address.
type machine struct {
	mac     string
	Text    string   // Concatenation of attribute terms.
	Classes []string // Vector of generic OS values.
}

type bayesClassifier struct {
	name               string
	set                []machine
	classifiers        map[string]*multibayes.Classifier
	level              int
	certainAbove       float64
	uncertainBelow     float64
	unknownValue       string
	classificationProp string
	TargetValue        func(rdi RecordedDeviceInfo) string
}

func uniqueWords(words []string) []string {
	dict := make(map[string]bool)

	list := []string{}
	for _, entry := range words {
		if _, value := dict[entry]; !value {
			dict[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

func (m *bayesClassifier) GenSetFromDB(B *backdrop, ifLookup string) error {
	rows, err := B.db.Queryx("SELECT * FROM device;")
	if err != nil {
		return errors.Wrap(err, "select device failed")
	}

	defer rows.Close()

	n := 0

	for rows.Next() {
		rdi := RecordedDeviceInfo{}

		err = rows.StructScan(&rdi)
		if err != nil {
			log.Printf("device scan failed: %v\n", err)
			continue
		}

		target := m.TargetValue(rdi)

		// Query training set for this DGroupID.
		trows, err := B.db.Queryx("SELECT * FROM training WHERE dgroup_id = $1", rdi.DGroupID)
		if err != nil {
			return errors.Wrap(err, "select training failed")
		}
		defer trows.Close()

		var paragraph strings.Builder

		for trows.Next() {
			rt := RecordedTraining{}

			err = trows.StructScan(&rt)
			if err != nil {
				log.Printf("training scan failed: %v\n", err)
				continue
			}

			_, sentence := genBayesSentenceFromDeviceInfoFile(B.ouidb,
				infofileFromTraining(rt, ifLookup))
			paragraph.WriteString(sentence)
			paragraph.WriteString(" ")
		}

		words := uniqueWords(strings.Fields(paragraph.String()))

		m.set = append(m.set, machine{rdi.DeviceMAC, strings.Join(words, " "),
			[]string{target}})
		n++

		trows.Close()
	}

	log.Printf("model has %d rows, set has %d machines", n, len(m.set))
	rows.Close()

	return nil
}

func (m *bayesClassifier) instancesTrainSpecifiedSplit() ([]machine, []machine) {
	if len(m.set) == 0 {
		log.Printf("empty source machine set from %s", m.name)
	}

	trainingRows := make([]machine, 0)
	testingRows := make([]machine, 0)

	// Create the return structure
	for _, s := range m.set {
		if len(s.Classes) == 1 && s.Classes[0] != m.unknownValue {
			log.Printf("machine -> training: %+v", s)
			trainingRows = append(trainingRows, s)
		} else {
			log.Printf("machine -> testing: %+v", s)
			testingRows = append(testingRows, s)
		}
	}

	log.Printf("training set size %d (%f)", len(trainingRows), float64((1.*len(trainingRows))/len(m.set)))

	return trainingRows, testingRows
}

func (m *bayesClassifier) train(B *backdrop, trainData []machine) {
	for _, machine := range trainData {
		for _, cl := range m.classifiers {
			cl.Add(machine.Text, machine.Classes)
		}
	}

	for k, cl := range m.classifiers {
		jm, err := cl.MarshalJSON()
		if err == nil {
			log.Printf("Model:\n%s\n", string(jm))
		} else {
			log.Printf("Cannot marshal '%s' classifier to JSON", k)
		}

		_, ierr := B.modeldb.Exec("INSERT OR REPLACE INTO model (generation_date, name, classifier_type, classifier_level, multibayes_min, certain_above, uncertain_below, model_json) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);",
			time.Now(), k, "bayes", m.level, cl.MinClassSize, m.certainAbove, m.uncertainBelow, jm)
		if ierr != nil {
			log.Printf("could not update '%s' model: %s", k, ierr)
		}
	}
}

func reviewBayes(m RecordedClassifier) string {
	var msg strings.Builder

	fmt.Fprintf(&msg, "Bayesian Classifier, Name: %s\nGenerated: %s\nCutoff: %d\n", m.ModelName,
		m.GenerationTS, m.MultibayesMin)
	dec := json.NewDecoder(strings.NewReader(m.ModelJSON))
	cls := make(map[string]int)

	for {
		var v map[string]interface{}

		if err := dec.Decode(&v); err != nil {
			break
		}

		for j := range v {
			if j == "matrix" {
				w := v["matrix"].(map[string]interface{})
				for k := range w {
					if k == "classes" {
						x := w["classes"].(map[string]interface{})
						for l := range x {
							y := x[l].([]interface{})
							cls[l] = len(y)
						}
					}
				}
			}
		}
	}

	for k, v := range cls {
		s := "\u2714" // checkmark
		if v < m.MultibayesMin {
			s = "\u2717" // ballot x
		}
		fmt.Fprintf(&msg, "%s %30s %4d\n", s, k, v)
	}

	return msg.String()
}
