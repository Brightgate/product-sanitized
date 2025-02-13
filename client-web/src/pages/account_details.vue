<!--
   Copyright 2019 Brightgate Inc.

   This Source Code Form is subject to the terms of the Mozilla Public
   License, v. 2.0. If a copy of the MPL was not distributed with this
   file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<style scoped>
div.acct-info-flex {
  display: flex;
  font-size: 16px;
}
div.avatar {
  margin-right: 10px;
  margin-top: 8px;
}
</style>
<template>
  <f7-page @page:beforein="onPageBeforeIn">
    <f7-navbar :back-link="$t('message.general.back')" :title="$t('message.account_details.title')" sliding />

    <f7-block>
      <h1>
        {{ acct.name }}
      </h1>
      <div class="acct-info-flex">
        <vue-avatar :src="acct.hasAvatar ? `/api/account/${acct.accountUUID}/avatar` : undefined" :username="acct.name" :size="64" class="avatar" inline />
        <div>
          {{ orgNameByID(acct.organizationUUID) }}
        </div>
      </div>
    </f7-block>

    <f7-list>
      <f7-list-item :title="'Profile'" group-title />
      <!-- Email -->
      <f7-list-item v-if="acct.email">
        <div slot="media"><f7-icon material="email" color="blue" /></div>
        <span>
          <f7-link :href="`mailto: ${acct.email}`" external>{{ acct.email }}</f7-link>
        </span>
      </f7-list-item>
      <f7-list-item v-else>
        <div slot="media"><f7-icon material="email" color="grey" /></div>
        {{ $t('message.account_details.email_none') }}
      </f7-list-item>

      <!-- Phone & SMS -->
      <f7-list-item v-if="acct.phoneNumber">
        <div slot="media"><f7-icon material="phone" color="blue" /></div>
        <div slot="title">
          <f7-link :href="`tel: ${acct.phoneNumber}`" external>{{ acct.phoneNumber }}</f7-link>
        </div>
        <div slot="after">
          <f7-link :href="`sms: ${acct.phoneNumber}`" external>
            <f7-icon material="textsms" color="blue" />
          </f7-link>
        </div>
      </f7-list-item>
      <f7-list-item v-else>
        <div slot="media"><f7-icon material="phone" color="grey" /></div>
        <div slot="title">
          {{ $t('message.account_details.phone_none') }}
        </div>
      </f7-list-item>

      <f7-list-item :title="$t('message.account_details.administration')" group-title />
      <f7-list-item :link="`${$f7route.url}roles/`">
        {{ $t('message.account_details.manage_roles') }}
      </f7-list-item>
      <f7-list-item>
        <span>{{ $t('message.account_details.wifi_login') }}<br>
          <small>
            <template v-if="sp && sp.status === 'unprovisioned'">
              {{ $t('message.account_details.not_provisioned') }}
            </template>
            <template v-if="sp && sp.status === 'provisioned'">
              {{ sp.username }}<br>
              {{ $t('message.account_details.last_provisioned', {last: formatTime(sp.completed)}) }}
            </template>
          </small>
        </span>
        <f7-button v-if="sp && sp.status === 'provisioned'"
                   color="red"
                   outline
                   @click="deprovision">
          {{ $t('message.account_details.deprovision_button') }}
        </f7-button>
      </f7-list-item>
      <!--
      <f7-list-item>
        <span>Suspend Account<br>
        <small>Prevents all forms of login</small></span>
        <f7-toggle />
      </f7-list-item>
      -->
      <f7-list-item>
        <span>{{ $t('message.account_details.delete_account') }}</span>
        <f7-button color="red" outline @click="confirmDelete">{{ $t('message.account_details.delete_button') }}</f7-button>
      </f7-list-item>

    </f7-list>
  </f7-page>
</template>
<script>
import Debug from 'debug';
import vuex from 'vuex';
import VueAvatar from 'vue-avatar';
import {format, parseISO} from '../date-fns-wrapper';
const debug = Debug('page:account_details');

export default {
  components: {
    'vue-avatar': VueAvatar,
  },

  data: function() {
    const accountID = this.$f7route.params.accountID;
    const acct = this.$store.getters.accountByID(accountID);
    return {
      acct: acct,
    };
  },

  computed: {
    // Map various $store elements as computed properties for use in the
    // template.
    ...vuex.mapGetters([
      'accountByID',
      'orgNameByID',
    ]),

    sp: function() {
      debug('sp, account is', this.acct);
      if (this.acct && this.acct.selfProvision) {
        return this.acct.selfProvision;
      }
      return null;
    },
  },

  methods: {
    confirmDelete: function() {
      const accountID = this.$f7route.params.accountID;
      const title = this.$t('message.account_details.delete_title');
      const text = this.$t('message.account_details.delete_text',
        {name: this.acct.name, org: this.orgNameByID(this.acct.organizationUUID)});
      this.$f7.dialog.confirm(text, title, () => {
        debug('proceeding to delete account');
        this.$store.dispatch('accountDelete', accountID);
        this.$f7router.back();
      });
    },

    onPageBeforeIn: function() {
      debug('onPageBeforeIn');
      const accountID = this.$f7route.params.accountID;
      this.$store.dispatch('fetchAccountSelfProvision', accountID);
      this.$store.dispatch('fetchAccountRoles', accountID);
    },

    deprovision: async function() {
      debug('deprovision');
      const accountID = this.$f7route.params.accountID;
      await this.$store.dispatch('accountDeprovision', accountID);
      await this.$store.dispatch('fetchAccountSelfProvision', accountID);
    },

    formatTime: function(t) {
      return format(parseISO(t), 'PPp');
    },
  },
};
</script>

