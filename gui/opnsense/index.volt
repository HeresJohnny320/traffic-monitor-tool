{# Reporting → Traffic Monitor; the dashboard comes through /traffic_monitor.php #}
<iframe id="traffic-monitor" src="/traffic_monitor.php?p=" title="Traffic Monitor"
        style="display:block;width:100%;height:calc(100vh - 160px);min-height:420px;border:0"></iframe>
<script>
// the dashboard fills the window down to its bottom edge (one scrollbar, inside)
(function () {
    var frame = document.getElementById("traffic-monitor");
    function fit() {
        var top = frame.getBoundingClientRect().top + window.pageYOffset;
        frame.style.height = Math.max(420, window.innerHeight - top - 12) + "px";
    }
    window.addEventListener("resize", fit);
    fit();
})();
</script>
