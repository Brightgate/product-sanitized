<!--
   Copyright 2020 Brightgate Inc.

   This Source Code Form is subject to the terms of the Mozilla Public
   License, v. 2.0. If a copy of the MPL was not distributed with this
   file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<style scoped>
div.shorter-block {
  margin: 16px 0;
}
</style>
<template>
  <f7-page ptr @ptr:refresh="pullRefresh">
    <f7-navbar :back-link="$t('message.general.back')" :title="$t('message.nodes.title')" sliding />
    <bg-site-breadcrumb :siteid="$f7route.params.siteID" />

    <f7-list class="shorter-block">
      <f7-list-item v-for="(node, nodeID) of nodes"
                    :key="nodeID"
                    :title="node.name || $t('message.nodes.unnamed_hw', {id: nodeID})"
                    :link="`${$f7route.url}${nodeID}/`"
                    media-item>
        <div slot="media">
          <bg-hw-icon :model="node.hwModel" width="48px" height="48px" />
        </div>
        <div slot="subtitle">
          {{ node.role === "gateway" ?
            $t('message.nodes.gateway_role') :
            $t('message.nodes.satellite_role')
          }}
        </div>
      </f7-list-item>
    </f7-list>

  </f7-page>
</template>
<script>
import Vuex from 'vuex';
import BGSiteBreadcrumb from '../components/site_breadcrumb.vue';
import BGHWIcon from '../components/hw_icon.vue';

export default {
  components: {
    'bg-site-breadcrumb': BGSiteBreadcrumb,
    'bg-hw-icon': BGHWIcon,
  },

  computed: {
    // Map various $store elements as computed properties for use in the
    // template.
    ...Vuex.mapGetters([
      'nodes',
    ]),
  },

  methods: {
    pullRefresh: async function(done) {
      try {
        await this.$store.dispatch('fetchNodes');
      } finally {
        done();
      }
    },
  },
};
</script>

