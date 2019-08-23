/*
 * COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */
import assert from 'assert';

import {cloneDeep, filter, keyBy, pickBy} from 'lodash-es';
import Promise from 'bluebird';
import retry from 'bluebird-retry';
import Vue from 'vue';
import Vuex from 'vuex';
import Debug from 'debug';

import appDefs from './app_defs';
import siteApi from './api/site';

const debug = Debug('store');

Vue.use(Vuex);

// XXX this needs further rationalization with devices.json
const DEVICE_CATEGORY_ALL = ['recent', 'phone', 'computer', 'printer', 'media', 'iot', 'unknown'];
const RETRY_DELAY = 1000;
const LOCAL_SITE_ID = '0';
const LOCAL_ORG_ID = '0';
const LOCAL_REGINFO = {
  UUID: LOCAL_SITE_ID,
  name: 'Local Site',
  organizationUUID: LOCAL_ORG_ID,
};

let i18n = null;
export function setStoreI18n(i) {
  i18n = i;
}

class Org {
  constructor(id) {
    debug(`new Org id=${id}`);
    assert.equal(typeof id, 'string');
    this.id = id;
    this.name = '';
    if (this.id === LOCAL_ORG_ID) {
      this.relationship = 'self';
    } else {
      this.relationship = 'none';
    }
    // List of account UUIDs for this Org
    this.accounts = [];
    debug(`done new Org id=${id}`);
  }
}

function getOrg(state, orgID) {
  assert(orgID !== undefined, 'orgID is undefined');
  if (state.orgs[orgID] === undefined) {
    // Make up a garbage org which can be used to swallow up the
    // results of whatever operation is ongoing-- this helps to
    // gracefully handle edge cases when the list of orgs is
    // changing and we have asynchronous completions for orgs no
    // longer in the orgs dictionary.
    return new Org(orgID);
  }
  return state.orgs[orgID];
}

class Site {
  constructor(id) {
    assert.equal(typeof id, 'string');
    debug(`new Site id=${id}`);
    this.id = id;
    // registry Information
    if (this.id === LOCAL_SITE_ID) {
      this._regInfo = cloneDeep(LOCAL_REGINFO);
    } else {
      this._regInfo = {};
    }
    this._devices = [];
    // Run the devices setter
    this.devices = [];
    this.alerts = [];
    this.rings = {};
    this.users = {};
    this.networkConfig = {};
    this.vaps = {};
    this.wan = {};
    this.health = {};
    debug(`done new Site id=${id}`);
  }

  get name() {
    return this.regInfo.name ? this.regInfo.name : this.id;
  }

  get devices() {
    return this._devices;
  }

  set regInfo(val) {
    assert(typeof val === 'object');
    assert(val.name !== undefined);
    assert(val.UUID !== undefined);
    assert(val.organizationUUID !== undefined);
    this._regInfo = val;
  }

  get regInfo() {
    return this._regInfo;
  }

  // Setting devices sets off a cascade of updates.
  set devices(val) {
    debug(`set devices: site ${this.id}`);
    assert(Array.isArray(val));
    this._devices = val;

    // First, organize by unique id.
    Vue.set(this, 'devicesByUniqID', keyBy(this._devices, 'uniqid'));

    // Next, Reorganize the data into:
    // { 'phone': [list of phones...], 'computer': [...] ... }
    //
    // Make sure all categories are present.
    const byCat = {};
    for (const c of DEVICE_CATEGORY_ALL) {
      byCat[c] = [];
    }

    this._devices.reduce((result, value) => {
      assert(value.category in byCat, `category ${value.category} is missing`);
      result[value.category].push(value);
      return result;
    }, byCat);
    Vue.set(this, 'devicesByCategory', byCat);

    // Index by ring
    const byRing = {};
    this._devices.reduce((result, value) => {
      if (result[value.ring] === undefined) {
        result[value.ring] = [];
      }
      result[value.ring].push(value);
      return result;
    }, byRing);
    Vue.set(this, 'devicesByRing', byRing);

    // Tabulate vulnerability counts and create vulnerability alerts for each
    // device
    const alerts = [];
    this._devices.forEach((device) => {
      const actives = pickBy(device.vulnerabilities, {active: true});
      Vue.set(device, 'activeVulnCount', Object.keys(actives).length);

      // Today all of the alerts we make are derived from the devices
      // list.  In the future, that could change.
      if (device.vulnerabilities) {
        for (const [vulnid, vulninfo] of Object.entries(device.vulnerabilities)) {
          alerts.push({
            'deviceID': device.uniqid,
            'vulnid': vulnid,
            'vulninfo': vulninfo,
          });
        }
      }
    });
    Vue.set(this, 'alerts', alerts);
    debug(`set devices: site ${this.id} completed`);
  }
}

function getSite(state, siteID) {
  if (state.sites[siteID] === undefined) {
    // Make up a garbage site which can be used to swallow up the
    // results of whatever operation is ongoing-- this helps to
    // gracefully handle edge cases when the list of sites is
    // changing and we have asynchronous completions of sites no
    // longer in the sites dictionary.
    return new Site(siteID);
  }
  return state.sites[siteID];
}

const nullSite = new Site('nullSite');

const state = {
  appMode: appDefs.APPMODE_FAILURE,
  authProviders: [],
  authProvidersError: null,
  testAppMode: appDefs.APPMODE_NONE,
  loggedIn: false,
  fakeLogin: false,
  mock: false,
  leftPanelVisible: false,
  sites: {},
  currentSiteID: nullSite.id,
  currentSite: nullSite,
  orgs: {},
  currentOrg: null,
  currentOrgID: null,
  userID: {},
  accounts: {},
  myAccountUUID: null,
};

function getAccount(state, accountID) {
  if (state.accounts[accountID] === undefined) {
    Vue.set(state.accounts, accountID, {});
  }
  return state.accounts[accountID];
}

function computeAppMode(state) {
  return state.testAppMode === appDefs.APPMODE_NONE ? state.appMode : state.testAppMode;
}

function accountHasOrgRole(state, accountUUID, orgUUID, role) {
  assert.equal(typeof accountUUID, 'string', 'bad accountUUID');
  assert.equal(typeof orgUUID, 'string', 'bad orgUUID');
  assert(appDefs.ALL_ROLES.includes(role), 'unrecognized role');
  const account = state.accounts[accountUUID];
  if (!account || !account.roles) {
    return false;
  }
  debug('accountHasOrgRoles', accountUUID, orgUUID, role, account.roles);
  const found = account.roles.findIndex((aor) => {
    if (aor.targetOrganization !== orgUUID) {
      return false;
    }
    return aor.roles.includes(role);
  });
  debug('accountHasOrgRoles found is', found);
  return found !== -1;
}

function siteHasRole(state, siteUUID, role) {
  assert.equal(typeof siteUUID, 'string');
  assert(appDefs.ALL_ROLES.includes(role), 'unrecognized role');
  if (siteUUID === LOCAL_SITE_ID) {
    return true;
  }
  const site = state.sites[siteUUID];
  if (!site) {
    return false;
  }
  const org = site.regInfo.organizationUUID;
  if (!org) {
    return false;
  }
  return accountHasOrgRole(state, state.myAccountUUID, org, role);
}

const mutations = {
  setSites(state, newSites) {
    debug('setSites: newSites', newSites);
    assert(Array.isArray(newSites));
    const newSitesDict = {};
    newSites.forEach((regInfo) => {
      // If the site exists already, grab that one.
      assert.equal(typeof regInfo.UUID, 'string');
      const siteID = regInfo.UUID;
      const site = state.sites[siteID] ? state.sites[siteID] : new Site(siteID);
      site.regInfo = regInfo;
      Vue.set(newSitesDict, siteID, site);
    });
    debug('setSites: newSitesDict', newSitesDict);
    Vue.set(state, 'sites', newSitesDict);
    // If there's only one site, default to it.
    if (newSites.length === 1) {
      debug('setSites: newSites.length is 1, updating currentSiteID');
      state.currentSiteID = newSites[0].UUID;
      state.currentSite = state.sites[state.currentSiteID];
    }
    // If the current site ID is gone (this should be rare, but it can
    // definitely happen when switching from 'local' to 'cloud' mock modes).
    if (state.sites[state.currentSiteID] === undefined) {
      debug('setSites: using nullSite');
      state.currentSiteID = nullSite.id;
      state.currentSite = nullSite;
    }
    debug('setSites: completed');
  },

  setOrgs(state, newOrgs) {
    debug(`setOrgs: newOrgs, currentOrg=${state.currentOrgID}`, newOrgs);
    const newOrgsDict = {};
    let homeOrg = null;
    newOrgs.forEach((apiOrg) => {
      const org = getOrg(state, apiOrg.organizationUUID);
      org.name = apiOrg.name;
      org.relationship = apiOrg.relationship;
      if (org.relationship === 'self') {
        homeOrg = org;
      }
      newOrgsDict[org.id] = org;
    });
    // Although it should be super rare, shoot down sites which are now
    // orphaned by loss of an org
    debug('setOrgs: newOrgsDict', newOrgsDict);
    for (const [id, site] of Object.entries(state.sites)) {
      if (!newOrgsDict[site.regInfo.organizationUUID]) {
        debug('setOrgs: shooting down orphan site!', site, site.regInfo.organizationUUID);
        Vue.delete(state.sites, id);
      }
    }
    // Cope with loss of org which is the current one
    if (newOrgsDict[state.currentOrgID] === undefined) {
      debug('setOrgs: seems like we lost currentOrgID', state.currentOrgID);
      state.currentOrg = homeOrg;
      state.currentOrgID = homeOrg ? homeOrg.id : null;
    }
    Vue.set(state, 'orgs', newOrgsDict);
    debug('setOrgs: completed');
  },

  setAppMode(state, newMode) {
    state.appMode = newMode;
  },

  setAuthProviders(state, newProviders) {
    state.authProviders = newProviders;
  },
  setAuthProvidersError(state, err) {
    state.authProvidersError = err;
  },

  setCurrentSiteID(state, newID) {
    debug('setCurrentSiteID', newID);
    if (state.sites[newID] === undefined) {
      debug(`Failed to set current site to unknown site ${newID}`);
      return;
    }
    state.currentSiteID = newID;
    state.currentSite = state.sites[state.currentSiteID];
    debug('setCurrentSiteID: currentSiteID now', state.currentSiteID);
    debug('setCurrentSiteID: currentSite now', state.currentSite);
  },

  setSiteDevices(state, {id, devices}) {
    getSite(state, id).devices = devices;
  },

  setSiteHealth(state, {id, health}) {
    getSite(state, id).health = health;
  },

  setSiteRings(state, {id, rings}) {
    getSite(state, id).rings = rings;
  },

  setSiteVAPs(state, {id, vaps}) {
    getSite(state, id).vaps = vaps;
  },

  setSiteWan(state, {id, wan}) {
    getSite(state, id).wan = wan;
  },

  setAccountSelfProvision(state, {accountID, sp}) {
    assert.equal(typeof accountID, 'string');
    assert.equal(typeof sp, 'object');
    Vue.set(getAccount(state, accountID), 'selfProvision', sp);
  },

  setAccountRoles(state, {accountID, roles}) {
    assert.equal(typeof accountID, 'string');
    assert(Array.isArray(roles), 'expected roles to be array');
    Vue.set(getAccount(state, accountID), 'roles', roles);
  },

  setSiteNetworkConfig(state, {id, networkConfig}) {
    getSite(state, id).networkConfig = networkConfig;
  },

  setSiteUsers(state, {id, users}) {
    debug('setSiteUsers', id, users);
    assert(users);
    Vue.set(getSite(state, id), 'users', users);
  },

  setSiteUser(state, {id, user}) {
    assert(user.UUID);
    getSite(state, id).users[user.UUID] = user;
  },

  setOrgAccounts(state, {orgID, acctList}) {
    debug('setOrgAccounts', orgID, acctList);
    assert(Array.isArray(acctList));
    Vue.set(getOrg(state, orgID), 'accounts', acctList);
  },

  setUserID(state, userID) {
    state.userID = userID;
    state.myAccountUUID = userID.accountUUID;
  },

  setAccountInfo(state, account) {
    debug('setAccountInfo', account);
    const id = account.accountUUID;
    assert.equal(typeof id, 'string');
    // Merge info from old account info into new, since extended
    // account info also gets decorated here as we obtain it.
    //
    // Must assign to new object to make reactive
    Vue.set(state.accounts, id, Object.assign({}, state.accounts[id], account));
  },

  accountDelete(state, accountUUID) {
    debug('accountDelete', accountUUID);
    assert.equal(typeof accountUUID, 'string');
    const orgUUID = getAccount(state, accountUUID).organization;
    debug('accountDelete orgUUID', orgUUID);
    const org = getOrg(state, orgUUID);
    debug('accountDelete org', org);
    const idx = org.accounts.findIndex((elem) => elem === accountUUID);
    if (idx !== -1) {
      Vue.delete(org.accounts, idx);
    }
    Vue.delete(state.accounts, accountUUID);
    debug('accounts', state.accounts);
  },

  setLoggedIn(state, newValue) {
    state.loggedIn = newValue;
  },

  setVulnRepair(state, {id, deviceID, vulnID, value}) {
    const app = getSite(state, id);
    const vuln = app && app.devicesByUniqID && app.devicesByUniqID[deviceID] &&
      app.devicesByUniqID[deviceID].vulnerabilities &&
      app.devicesByUniqID[deviceID].vulnerabilities[vulnID];
    if (!vuln) {
      debug('failed to get vulnerability', id, deviceID, vulnID);
      return;
    }
    Vue.set(vuln, 'repair', true);
  },

  setTestAppMode(state, newMode) {
    assert([appDefs.APPMODE_CLOUD, appDefs.APPMODE_LOCAL, appDefs.APPMODE_NONE].includes(newMode));
    state.testAppMode = newMode;
  },

  setMock(state, newValue) {
    state.mock = newValue;
    debug('setMock', newValue, computeAppMode(state));
    if (state.mock) {
      if (computeAppMode(state) === appDefs.APPMODE_CLOUD) {
        siteApi.setMockMode(appDefs.APPMODE_CLOUD);
      } else {
        siteApi.setMockMode(appDefs.APPMODE_LOCAL);
      }
    } else {
      siteApi.setMockMode(appDefs.APPMODE_NONE);
    }
  },

  setFakeLogin(state, newValue) {
    state.fakeLogin = newValue;
  },

  setLeftPanelVisible(state, newValue) {
    state.leftPanelVisible = newValue;
  },
};

const getters = {
  loggedIn: (state) => state.loggedIn || state.fakeLogin,
  fakeLogin: (state) => state.fakeLogin,
  mock: (state) => state.mock,
  currentSiteID: (state) => state.currentSiteID,
  leftPanelVisible: (state) => state.leftPanelVisible,
  authProviders: (state) => state.authProviders,
  authProvidersError: (state) => state.authProvidersError,
  userID: (state) => state.userID,
  myAccountUUID: (state) => state.myAccountUUID,

  myAccount: (state) => {
    return state.accounts[state.myAccountUUID];
  },

  siteAlerts: (state) => (siteID) => {
    return getSite(state, siteID).alerts;
  },
  alerts: (state) => {
    return state.currentSite.alerts;
  },

  org: (state) => {
    return state.currentOrg;
  },

  siteDevices: (state) => (siteID) => {
    return getSite(state, siteID).devices;
  },
  devices: (state) => {
    return state.currentSite.devices;
  },

  siteDeviceByUniqID: (state) => (siteID, uniqid) => {
    return getSite(state, siteID).devicesByUniqID[uniqid];
  },
  deviceByUniqID: (state) => (uniqid) => {
    return state.currentSite.devicesByUniqID[uniqid];
  },

  siteHealth: (state) => (siteID) => {
    return getSite(state, siteID).health;
  },
  health: (state) => {
    return state.currentSite.health;
  },

  appMode: (state) => {
    return computeAppMode(state);
  },

  testAppMode: (state) => {
    return state.testAppMode;
  },

  siteDevicesByCategory: (state) => (siteID, category) => {
    return getSite(state, siteID).devicesByCategory[category];
  },
  devicesByCategory: (state) => (category) => {
    return state.currentSite.devicesByCategory[category];
  },

  siteDevicesByRing: (state) => (siteID, ring) => {
    return getSite(state, siteID).devicesByRing[ring] || [];
  },
  devicesByRing: (state) => (ring) => {
    return state.currentSite.devicesByRing[ring] || [];
  },

  siteNetworkConfig: (state) => (siteID) => {
    return getSite(state, siteID).networkConfig;
  },
  networkConfig: (state) => {
    return state.currentSite.networkConfig;
  },

  siteRings: (state) => (siteID) => {
    return getSite(state, siteID).rings;
  },
  rings: (state) => {
    return state.currentSite.rings;
  },

  siteAdmin: (state) => {
    return siteHasRole(state, state.currentSiteID, appDefs.ROLE_ADMIN);
  },

  currentOrgAdmin: (state) => {
    if (!state.currentOrgID) {
      return false;
    }
    return accountHasOrgRole(state, state.myAccountUUID, state.currentOrgID, appDefs.ROLE_ADMIN);
  },

  accountHasOrgRole: (state) => (account, org, role) => {
    return accountHasOrgRole(state, account, org, role);
  },

  siteVAPs: (state) => (siteID) => {
    return getSite(state, siteID).vaps;
  },
  vaps: (state) => {
    return state.currentSite.vaps;
  },

  siteWan: (state) => (siteID) => {
    return getSite(state, siteID).wan;
  },
  wan: (state) => {
    return state.currentSite.wan;
  },

  siteUsers: (state) => (siteID) => {
    return getSite(state, siteID).users;
  },
  users: (state) => {
    return state.currentSite.users;
  },

  siteUserByUUID: (state) => (siteID, uuid) => {
    return getSite(state, siteID).users[uuid];
  },
  userByUUID: (state) => (uuid) => {
    return state.currentSite.users[uuid];
  },

  sites: (state) => {
    return state.sites;
  },
  siteByID: (state) => (id) => {
    return state.sites[id];
  },

  orgs: (state) => {
    return state.orgs;
  },
  orgByID: (state) => (id) => {
    return getOrg(state, id);
  },
  orgNameBySiteID: (state) => (id) => {
    const site = state.sites[id];
    if (site && site.regInfo && site.regInfo.organizationUUID) {
      const org = getOrg(state, site.regInfo.organizationUUID);
      return org.name;
    }
    return i18n.t('message.api.unknown_org');
  },
  orgNameByID: (state) => (id) => {
    const org = getOrg(state, id);
    if (org && org.name) {
      return org.name;
    }
    return i18n.t('message.api.unknown_org');
  },

  // List of account UUIDs for current org
  accountList: (state) => {
    return state.currentOrg ? state.currentOrg.accounts : [];
  },

  // Account data by account UUID
  accountByID: (state) => (id) => {
    return state.accounts[id];
  },

  // device utility functions
  // XXX since these don't reference state explicitly, they should move to
  // a library, probably.
  deviceCount: (state) => (devices) => {
    assert(Array.isArray(devices), 'expected devices to be array');
    return devices.length;
  },
  deviceActive: (state) => (devices) => {
    return filter(devices, {active: true});
  },
  deviceVulnScanned: (state) => (devices) => {
    return filter(devices, 'scans.vuln.finish');
  },
  deviceVulnerable: (state) => (devices) => {
    return filter(devices, 'activeVulnCount');
  },
  deviceNotVulnerable: (state) => (devices) => {
    return filter(devices, {activeVulnCount: 0});
  },

  // alert utility functions
  alertCount: (state) => (alerts) => {
    assert(typeof(alerts) === 'object' && !Array.isArray(alerts), 'expected alerts to be object');
    return Object.keys(alerts).length;
  },
  alertActive: (state) => (alerts) => {
    return pickBy(alerts, {vulninfo: {active: true}});
  },
  alertInactive: (state) => (alerts) => {
    return pickBy(alerts, {vulninfo: {active: false}});
  },
  alertByRing: (state) => (ring, alerts) => {
    return pickBy(alerts, {device: {ring: ring}});
  },

  // user utility functions
  userCount: (state) => (users) => {
    assert(typeof(users) === 'object' && !Array.isArray(users), 'expected users to be object');
    return Object.keys(users).length;
  },
};

// Take an API device and transform it for local use.
//
// Today this is concerned with deriving local state for device categorization
// and identity.  We expect a lot of this could will change drastically when
// we revise our device identity system.
function computeDeviceProps(device) {
  if (!device.displayName) {
    device.displayName = i18n.t('message.api.unknown_device', {hwAddr: device.hwAddr});
  }
  // uniqid is used in sorting and categorization
  device.uniqid = device.hwAddr;

  const k2c = {
    'android': 'phone',
    'ios': 'phone',
    'computer': 'computer',
    'iot': 'iot',
    'unknown': 'unknown',
    'media': 'media',
    'printer': 'printer',
  };
  const k2m = {
    'android': 'mobile-phone-1',
    'ios': 'mobile-phone-1',
    'computer': 'laptop-1',
    'iot': 'webcam-1',
    'unknown': 'misc-device',
    'media': 'television',
    'printer': 'tablet', // XXX for now
  };
  assert(typeof(device.confidence) === 'number');
  // derived from logic in configctl
  if (device.confidence < 0.5) {
    device.category = 'unknown';
    device.media = k2m['unknown'];
    device.certainty = 'low';
  } else {
    device.certainty = device.confidence < 0.87 ? 'medium' : 'high';
    device.category = device.kind in k2c ? k2c[device.kind] : k2c['unknown'];
    device.media = device.kind in k2m ? k2m[device.kind] : k2m['unknown'];
  }
  return device;
}

let fetchDevicesPromise = Promise.resolve();
let fetchPeriodicTimeout = null;

const actions = {
  // Load the list of sites from the server.
  async fetchSites(context) {
    debug('fetchSites');
    // We'll compare later to see if we should trigger more work
    const siteID = context.state.currentSiteID;
    const sites = await siteApi.sitesGet();
    debug('fetchSites: got', sites);
    context.commit('setSites', sites);
    const newSiteID = context.state.currentSiteID;
    if (siteID !== newSiteID) {
      debug('fetchSites: siteid changed, triggering fetch');
      context.dispatch('fetchSiteChanged');
    }
  },

  async fetchSiteHealth(context) {
    if (context.state.appMode !== appDefs.APPMODE_CLOUD) {
      debug('fetchSiteHealth: skipped, not cloud');
      return;
    }
    if (context.state.currentSite === nullSite) {
      debug('fetchSiteHealth: skipped, nullSite');
      return;
    }
    const id = context.state.currentSiteID;
    const health = await siteApi.siteHealthGet(id);
    context.commit('setSiteHealth', {id: id, health: health});
  },

  async setCurrentSiteID(context, {id}) {
    context.commit('setCurrentSiteID', id);
    await context.dispatch('fetchPeriodicStop');
    // Re-get the world
    context.dispatch('fetchSiteChanged');
  },

  // Load the list of devices from the server.
  fetchDevices(context) {
    const id = context.state.currentSiteID;
    debug(`fetchDevices: site ${id}`);
    // Join to existing fetch, so that only one fetch is ongoing
    // Important: we await the fetch, and then drive on, because
    // the ID might have changed, and so we want to process this
    // fetch too.
    let p = null;
    if (fetchDevicesPromise.isPending()) {
      debug('fetchDevices: chaining onto pending fetchDevices');
      p = fetchDevicesPromise;
    } else {
      p = Promise.resolve();
    }
    if (context.state.currentSite === nullSite) {
      debug('fetchDevices: skipped, nullSite');
      return p;
    }
    if (!siteHasRole(state, id, appDefs.ROLE_ADMIN)) {
      debug('fetchDevices: skipped, not an admin');
      return p;
    }
    let devices = [];
    fetchDevicesPromise = p.then(() => {
      return retry(siteApi.siteDevicesGet, {
        interval: RETRY_DELAY,
        max_tries: 5, // eslint-disable-line camelcase
        args: [id],
      }).then((apiDevices) => {
        devices = apiDevices.map(computeDeviceProps);
        context.commit('setSiteDevices', {id: id, devices: devices});
      }).tapCatch((err) => {
        debug('fetchDevices: failed', err);
      });
    });
    return fetchDevicesPromise;
  },

  // Start a timer-driven periodic fetch of devices
  fetchPeriodic(context) {
    if (fetchPeriodicTimeout !== null) {
      clearTimeout(fetchPeriodicTimeout);
      fetchPeriodicTimeout = null;
    }
    // if not logged in, just stop.
    const siteID = context.state.currentSiteID;
    if (!context.getters.loggedIn ||
       !siteHasRole(state, siteID, appDefs.ROLE_ADMIN)) {
      debug('fetchPeriodic: not logged in or not admin, disabling');
      return;
    }

    debug('fetchPeriodic: dispatching fetchSiteHealth (async)');
    context.dispatch('fetchSiteHealth');

    debug('fetchPeriodic: dispatching fetchDevices');
    context.dispatch('fetchDevices'
    ).then(() => {
      fetchPeriodicTimeout = setTimeout(() => {
        context.dispatch('fetchPeriodic');
      }, 10000);
    }, () => {
      debug('fetchPeriodic: failed, back in 30');
      fetchPeriodicTimeout = setTimeout(() => {
        context.dispatch('fetchPeriodic');
      }, 30000);
    });
    return;
  },

  fetchPeriodicStop(context) {
    clearTimeout(fetchPeriodicTimeout);
    fetchPeriodicTimeout = null;
  },

  // Load the list of rings from the server.
  async fetchRings(context) {
    if (context.state.currentSite === nullSite) {
      debug('fetchRings: skipped, nullSite');
      return;
    }
    const id = context.state.currentSiteID;
    const rings = await siteApi.siteRingsGet(id);
    context.commit('setSiteRings', {id: id, rings: rings});
  },

  async fetchAccountSelfProvision(context, accountID) {
    if (context.state.appMode !== appDefs.APPMODE_CLOUD) {
      return;
    }
    assert.equal(typeof accountID, 'string');
    const sp = await siteApi.accountSelfProvisionGet(accountID);
    debug('fetchAccountSelfProvision: accountID, sp', accountID, sp);
    context.commit('setAccountSelfProvision', {accountID: accountID, sp: sp});
  },

  async accountDeprovision(context, accountID) {
    if (context.state.appMode !== appDefs.APPMODE_CLOUD) {
      return;
    }
    debug('accountDeprovision: accountID', accountID);
    assert.equal(typeof accountID, 'string');
    try {
      await siteApi.accountDeprovisionPost(accountID);
    } finally {
      const sp = await siteApi.accountSelfProvisionGet(accountID);
      debug('accountDeprovision: refreshed sp info', sp);
      context.commit('setAccountSelfProvision', {accountID: accountID, sp: sp});
    }
  },

  async fetchAccountRoles(context, accountID) {
    if (context.state.appMode !== appDefs.APPMODE_CLOUD) {
      return;
    }
    assert.equal(typeof accountID, 'string');
    const roles = await siteApi.accountRolesGet(accountID);
    debug('fetchAccountRoles: accountID, roles', accountID, roles);
    context.commit('setAccountRoles', {accountID: accountID, roles: roles});
  },

  async updateAccountRoles(context, {accountID, tgtOrgUUID, role, value}) {
    if (context.state.appMode !== appDefs.APPMODE_CLOUD) {
      return;
    }
    assert.equal(typeof accountID, 'string');
    assert.equal(typeof tgtOrgUUID, 'string');
    assert.equal(typeof role, 'string');
    assert.equal(typeof value, 'boolean');
    await siteApi.accountRolesPost(accountID, tgtOrgUUID, role, value);
    await context.dispatch('fetchAccountRoles', accountID);
  },

  async accountDelete(context, accountID) {
    if (context.state.appMode !== appDefs.APPMODE_CLOUD) {
      return;
    }
    debug('accountDelete: accountID', accountID);
    assert.equal(typeof accountID, 'string');
    await siteApi.accountDelete(accountID);
    context.commit('accountDelete', accountID);
    context.dispatch('fetchOrgAccounts');
  },


  // Load the various aspects of the network configuration from the server.
  async fetchNetworkConfig(context) {
    if (context.state.currentSite === nullSite) {
      debug('fetchNetworkConfig: skipped, nullSite');
      return;
    }
    debug(`fetchNetworkConfig`);
    const id = context.state.currentSiteID;

    const wan = await siteApi.siteWanGet(id);
    debug('fetchNetworkConfig: committing wan', wan);
    context.commit('setSiteWan', {id, wan});

    const nc = await Promise.props({
      dnsServer: siteApi.siteConfigGet(id, '@/network/dnsserver', ''),
      baseAddress: siteApi.siteConfigGet(id, '@/network/base_address', ''),
    });
    debug('fetchNetworkConfig: committing', nc);
    context.commit('setSiteNetworkConfig', {id: id, networkConfig: nc});
    return nc;
  },

  // Load the various aspects of the network configuration from the server.
  async fetchVAPs(context) {
    if (context.state.currentSite === nullSite) {
      debug('fetchVAPs: skipped, nullSite');
      return;
    }
    const id = context.state.currentSiteID;
    const vaps = await siteApi.siteVAPsGet(id);
    context.commit('setSiteVAPs', {id: id, vaps: vaps});
  },


  async enrollGuest(context, {kind, phoneNumber, email}) {
    if (context.state.currentSite === nullSite) {
      debug('enrollGuest: skipped, nullSite');
      return;
    }
    const id = context.state.currentSiteID;
    return await siteApi.siteEnrollGuest(id, {kind, phoneNumber, email});
  },

  // Ask the server to change the ring property for a device, then
  // attempt to wait for that change to propagate.  In practice this
  // seems to take several seconds, during which time the server may
  // become unreachable; thus we use retrys to make things work properly.
  async changeRing(context, {deviceUniqID, newRing}) {
    if (context.state.currentSite === nullSite) {
      debug('changeRing: skipped, nullSite');
      return;
    }
    const id = context.state.currentSiteID;
    await siteApi.siteClientsRingSet(id, deviceUniqID, newRing);
    await context.dispatch('fetchDevices');
  },

  // Ask the server to repair a vulnerability by setting the appropriate
  // property.
  async repairVuln(context, {deviceID, vulnID}) {
    assert(typeof deviceID === 'string');
    assert(typeof vulnID === 'string');

    if (context.state.currentSite === nullSite) {
      debug('repairVuln: skipped, nullSite');
      return;
    }

    debug(`repairVuln: ${deviceID} ${vulnID}`);
    const id = context.state.currentSiteID;
    context.commit('setVulnRepair', {id: id, deviceID: deviceID, vulnID: vulnID, value: true});
    try {
      await siteApi.siteConfigSet(id, `@/clients/${deviceID}/vulnerabilities/${vulnID}/repair`, 'true');
    } catch (err) {
      debug('repairVuln: failed to set repair bit', err);
    } finally {
      context.dispatch('fetchDevices');
    }
  },

  async fetchUsers(context) {
    if (context.state.currentSite === nullSite) {
      debug('fetchUsers: skipped, nullSite');
      return;
    }
    const id = context.state.currentSiteID;
    const users = await siteApi.siteUsersGet(id);
    context.commit('setSiteUsers', {id: id, users: users});
  },

  async fetchOrgs(context) {
    const orgs = await siteApi.orgsGet();
    debug('fetchOrgs: got', orgs);
    context.commit('setOrgs', orgs);
  },

  async fetchOrgAccounts(context) {
    if (computeAppMode(state) !== appDefs.APPMODE_CLOUD) {
      return;
    }
    if (context.state.currentOrg === null) {
      debug('fetchOrgAccounts: skipped, null Org');
      return;
    }
    const orgID = context.state.currentOrgID;
    let accounts = null;
    try {
      accounts = await siteApi.orgAccountsGet(orgID);
    } catch (err) {
      debug('fetchOrgAccounts: failed orgAccountsGet', err);
      return;
    }

    const acctList = accounts.map((acct) => acct.accountUUID);
    context.commit('setOrgAccounts', {orgID: orgID, acctList: acctList});
    accounts.forEach((acct) => {
      acct.organizationUUID = orgID;
      context.commit('setAccountInfo', acct);
    });
  },

  // Create or Update a user
  async saveUser(context, {user, newUser}) {
    assert(typeof user === 'object');
    assert(typeof newUser === 'boolean');

    if (context.state.currentSite === nullSite) {
      debug('saveUser: skipped, nullSite');
      return;
    }
    const id = context.state.currentSiteID;
    const action = newUser ? 'creating' : 'updating';
    debug(`saveUser: ${action} ${user.UUID}`, user);
    if (newUser) {
      delete user.UUID; // Backend is strict about UUID
    }
    try {
      const postUser = await siteApi.siteUsersPost(id, user, newUser);
      context.commit('setSiteUser', {id: id, user: postUser});
    } catch (err) {
      debug('saveUser: failed', err);
      if (err.res && err.res.text) {
        throw new Error(`Failed to save user: ${err.res.text}`);
      } else {
        throw err;
      }
    }
  },

  async deleteUser(context, {user, newUser}) {
    assert(typeof user === 'object');
    if (context.state.currentSite === nullSite) {
      debug('deleteUser: skipped, nullSite');
      return;
    }
    const id = context.state.currentSiteID;
    await siteApi.siteUsersDelete(id, user.UUID);
    context.dispatch('fetchUsers');
  },

  async checkLogin(context) {
    let loggedin = false;
    try {
      const userID = await siteApi.authUserID();
      debug('checkLogin: got userID', userID);
      context.commit('setUserID', userID);
      loggedin = true;
    } catch (err) {
      loggedin = false;
    }
    context.commit('setLoggedIn', loggedin);
    debug(`checkLogin: ${loggedin}`);
    return loggedin;
  },

  async login(context, {uid, userPassword}) {
    assert.equal(typeof uid, 'string');
    assert.equal(typeof userPassword, 'string');
    await siteApi.authApplianceLogin(uid, userPassword);
    context.commit('setLoggedIn', true);
    const userID = await siteApi.authUserID();
    debug('login: got userID', userID);
    context.commit('setUserID', userID);
    // Let these run async
    context.dispatch('fetchPostLogin');
  },

  async fetchPostLogin(context) {
    debug('fetchPostLogin');
    await context.dispatch('fetchAccountRoles', context.state.myAccountUUID);
    context.dispatch('fetchOrgs');
    context.dispatch('fetchSites');
    context.dispatch('fetchOrgAccounts').catch(() => {});
  },

  async fetchSiteChanged(context) {
    debug('fetchSiteChanged');
    context.dispatch('fetchAccountRoles', context.state.myAccountUUID).catch(() => {});
    context.dispatch('fetchAccountSelfProvision', context.state.myAccountUUID).catch(() => {});
    context.dispatch('fetchSiteHealth').catch(() => {});
    context.dispatch('fetchDevices').catch(() => {});
    context.dispatch('fetchRings').catch(() => {});
    context.dispatch('fetchUsers').catch(() => {});
    context.dispatch('fetchVAPs').catch(() => {});
    context.dispatch('fetchPeriodic').catch(() => {});
  },

  async logout(context) {
    debug('logout');
    await context.dispatch('fetchPeriodicStop');
    siteApi.authApplianceLogout();
    context.commit('setLoggedIn', false);
    debug('logout: Completed');
  },

  async fetchProviders(context) {
    const providers = await siteApi.authProviders();
    debug('fetchProviders: got', providers);
    assert(providers.mode !== undefined);
    assert(providers.providers !== undefined);
    context.commit('setAppMode', providers.mode);
    context.commit('setAuthProviders', providers.providers);
    context.commit('setAuthProvidersError', providers.error || null);
  },
};

export const store = new Vuex.Store({
  strict: process.env.NODE_ENV !== 'production',
  actions,
  state,
  getters,
  mutations,
});

// At store startup, try to get the list of auth providers and appMode.
Promise.resolve().then(async () => {
  debug('Startup: Try to get auth providers and app Mode.');
  return store.dispatch('fetchProviders');
}).catch(() => {
  // XXX We will need to try harder in the future.
  debug('Startup: Failed to fetch auth providers and app Mode.');
});
