<?php
/*
 * Lets non-admin pfSense users be given access to Traffic Monitor
 * (System → User Manager → Effective Privileges). Installed by Traffic
 * Monitor's install.sh in /etc/inc/priv/.
 */
global $priv_list;

$priv_list['page-status-trafficmonitor'] = array();
$priv_list['page-status-trafficmonitor']['name'] = "WebCfg - Status: Traffic Monitor";
$priv_list['page-status-trafficmonitor']['descr'] = "Allow access to Traffic Monitor (view and change its settings).";
$priv_list['page-status-trafficmonitor']['match'] = array();
$priv_list['page-status-trafficmonitor']['match'][] = "status_traffic_monitor.php*";
$priv_list['page-status-trafficmonitor']['match'][] = "traffic_monitor.php*";
