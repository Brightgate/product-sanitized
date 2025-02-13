<!--
   Copyright 2020 Brightgate Inc.

   This Source Code Form is subject to the terms of the Mozilla Public
   License, v. 2.0. If a copy of the MPL was not distributed with this
   file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<template>
  <f7-page ptr @page:beforein="onPageBeforeIn" @ptr:refresh="pullRefresh">
    <f7-navbar :back-link="$t('message.general.back')" :title="$t('message.compliance_report.title')" sliding />
    <bg-site-breadcrumb :siteid="$f7route.params.siteID" />

    <f7-card :title="$t('message.compliance_report.summary')">
      <f7-list no-hairlines no-hairlines-between>
        <f7-list-item media>
          <div slot="media">
            <f7-icon v-if="policyViolations !== 0" f7="bolt_circle_fill" color="red" />
            <f7-icon v-else f7="checkmark_circle_fill" color="green" />
          </div>
          <span v-if="policyViolations !== 0" style="font-weight: bold">
            {{ $tc("message.compliance_report.summary_violations", policyViolations, {num: policyViolations}) }}
          </span>
          <span v-else>
            {{ $t("message.compliance_report.summary_no_violations") }}
          </span>
        </f7-list-item>

        <f7-list-item>
          {{ $t("message.compliance_report.summary_enrolled", {num: userCount(users)}) }}
          {{ $t("message.compliance_report.summary_phish", {num: phishingIncidents}) }}
        </f7-list-item>
        <f7-list-item>
          {{ $t("message.compliance_report.summary_vuln", {num: alertCount(alertActive(alerts))}) }}
        </f7-list-item>
      </f7-list>
    </f7-card>

    <f7-card v-if="alertCount(alertActive(alerts)) === 0"
             :title="$t('message.compliance_report.active_violations')"
             :content="$t('message.compliance_report.no_active_violations')" />
    <f7-card v-else :title="$t('message.compliance_report.active_violations')">
      <f7-list>
        <f7-list-item
          v-for="alert in alertActive(alerts)"
          :key="alert.deviceID + '-' + alert.vulnid"
          :link="`/sites/${$f7route.params.siteID}/devices/${alert.deviceID}/`">
          <span>
            <f7-icon f7="bolt_circle_fill" color="red" />
            {{ $t('message.alerts.problem_on_device',
                  {problem: vulnHeadline(alert.vulnid), device: deviceByUniqID(alert.deviceID).displayName})
            }}
          </span>
        </f7-list-item>
      </f7-list>
    </f7-card>

    <f7-card v-if="alertCount(alertInactive(alerts)) === 0"
             :title="$t('message.compliance_report.resolved_violations')"
             :content="$t('message.compliance_report.no_resolved_violations')" />
    <f7-card v-else :title="$t('message.compliance_report.resolved_violations')">
      <f7-list>
        <f7-list-item
          v-for="alert in alertInactive(alerts)"
          :key="alert.deviceID + '-' + alert.vulnid">
          <span>
            <f7-icon f7="bolt_circle_fill" color="gray" />
            {{ $t('message.alerts.problem_on_device',
                  {problem: vulnHeadline(alert.vulnid), device: deviceByUniqID(alert.deviceID).displayName})
            }}
          </span>
        </f7-list-item>
      </f7-list>
    </f7-card>

    <f7-card :title="$t('message.compliance_report.ring_summary')">
      <f7-block style="margin-top: 5px; font-size: 12pt;">
        <span style="color: rgba(0,0,0,0.5);">
          <f7-icon f7="checkmark_circle_fill" size="1em" color="green" />
          {{ $t('message.compliance_report.ring_ok') }}<br>
          <f7-icon f7="question_circle_fill" size="1em" color="orange" />
          {{ $t('message.compliance_report.ring_not_scanned') }}<br>
          <f7-icon f7="bolt_circle_fill" size="1em" color="red" />
          {{ $t('message.compliance_report.ring_vulnerable') }}<br>
          <f7-icon f7="circle" size="1em" color="gray" />
          {{ $t('message.compliance_report.ring_inactive') }}<br>
          <br>
        </span>

        <f7-row style="padding-top: 7px; padding-bottom: 7px;">
          <f7-col width="40">
            <!-- <f7-icon f7="data_fill" color="white"></f7-icon> -->
            {{ $t('message.compliance_report.population') }}
          </f7-col>
          <f7-col width="60" style="text-align: center">
            <bg-ring-summary :devices="devices" show-zero />
          </f7-col>
        </f7-row>

        <f7-row v-for="ring in SortedRingNames"
                :key="ring"
                style="padding-top: 7px; padding-bottom: 7px;">
          <f7-col width="40">
            {{ ring }}
          </f7-col>
          <f7-col width="60" style="text-align: center">
            <bg-ring-summary :devices="devicesByRing(ring)" />
          </f7-col>
        </f7-row>
      </f7-block>
    </f7-card>

  </f7-page>
</template>

<script>
import Vuex from 'vuex';
import {sortBy} from 'lodash-es';
import vulnerability from '../vulnerability';
import BGRingSummary from '../components/ring_summary.vue';
import BGSiteBreadcrumb from '../components/site_breadcrumb.vue';

export default {

  components: {
    'bg-ring-summary': BGRingSummary,
    'bg-site-breadcrumb': BGSiteBreadcrumb,
  },
  data: function() {
    return {
    };
  },

  computed: {
    // Map various $store elements as computed properties for use in the
    // template.
    ...Vuex.mapGetters([
      'alertActive',
      'alertCount',
      'alertInactive',
      'alerts',
      'deviceActive',
      'deviceByUniqID',
      'deviceCount',
      'deviceNotVulnerable',
      'devices',
      'devicesByRing',
      'devicesByRing',
      'deviceVulnerable',
      'deviceVulnScanned',
      'mock',
      'networkConfig',
      'rings',
      'userCount',
      'users',
    ]),
    phishingIncidents: function() {
      return 0;
    },
    policyViolations: function() {
      return this.alertCount(this.alertActive(this.alerts));
    },
    SortedRingNames: function() {
      return sortBy(Object.keys(this.rings), (r) => {
        return -1 * this.devicesByRing(r).length;
      });
    },
  },

  methods: {
    vulnHeadline: function(vulnid) {
      return vulnerability.headline(vulnid);
    },
    pullRefresh: async function(done) {
      try {
        await this.$store.dispatch('fetchNetworkConfig');
        await this.$store.dispatch('fetchDevices');
        await this.$store.dispatch('fetchRings');
      } finally {
        done();
      }
    },

    onPageBeforeIn: function() {
      this.$store.dispatch('fetchNetworkConfig').catch(() => {}),
      this.$store.dispatch('fetchDevices').catch(() => {});
      this.$store.dispatch('fetchRings').catch(() => {});
    },
  },
};
</script>

