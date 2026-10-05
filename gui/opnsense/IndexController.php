<?php

/*
 * Reporting → Traffic Monitor. Installed by Traffic Monitor's install.sh in
 * /usr/local/opnsense/mvc/app/controllers/OPNsense/TrafficMonitor/.
 */

namespace OPNsense\TrafficMonitor;

class IndexController extends \OPNsense\Base\IndexController
{
    public function indexAction()
    {
        $this->view->pick('OPNsense/TrafficMonitor/index');
    }
}
