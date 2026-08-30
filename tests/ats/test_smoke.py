"""Kind install and auth smoke for the standalone chart.

The smoke proves the chart on every PR, end to end, on the ATS kind cluster:

  1. prerequisites go on first (Gateway API CRDs, prerequisites/lab-dex.yaml),
     exactly like the README quick start;
  2. the candidate chart is installed with examples/kind-lab-dex.yaml (ATS's
     own pre-test deploy is skipped via app-tests-skip-app-deploy, because the
     chart cannot install before the prerequisites);
  3. every Deployment becomes Ready, an unauthenticated /mcp returns 401 with
     the WWW-Authenticate discovery chain, a login with a lab Dex static user
     reaches /mcp with 200 (the full muster OAuth flow: dynamic client
     registration, authorization code + PKCE, the Dex login form), and a
     kagent Agent reaches Ready against a fake model provider.

The upgrade scenario reuses the same helpers: ATS installs the last published
chart from the catalog, tests/ats/upgrade-hook.sh runs the documented CRD
re-apply one-liner, ATS upgrades to the candidate, and the post-upgrade stage
re-runs the readiness and auth assertions.

The edge Gateway Service is ClusterIP, so the tests reach it through a
`kubectl port-forward` on a high port. Hostnames stay the real
*.127.0.0.1.nip.io ones (nip.io resolves them to 127.0.0.1, where the forward
listens), so TLS verifies against the lab CA and the Gateway's hostname
matching sees the hostnames the routes declare.
"""

import base64
import hashlib
import html
import json
import logging
import re
import secrets
import socket
import subprocess  # nosec: kubectl port-forward has no API equivalent
import threading
import time
from pathlib import Path
from typing import Any, Dict, List, Optional, Set
from urllib.parse import parse_qs, urlencode, urljoin, urlsplit, urlunsplit

import pykube
import pytest
import requests
import yaml
from pytest_helm_charts.clusters import Cluster
from pytest_helm_charts.giantswarm_app_platform.app import (
    AppFactoryFunc,
    ConfiguredApp,
)

logger = logging.getLogger(__name__)

REPO_ROOT = Path(__file__).resolve().parents[2]
APP_NAME = "agent-platform-standalone"
NAMESPACE = "agent-platform"
KAGENT_NAMESPACE = "kagent"
DOMAIN = "127.0.0.1.nip.io"
CHARTMUSEUM_URL = "http://chartmuseum.giantswarm.svc.cluster.local.:8080/"
GATEWAY_API_CRDS = (
    "https://github.com/kubernetes-sigs/gateway-api/releases/download/"
    "v1.5.0/standard-install.yaml"
)
# Fixed lab credentials from prerequisites/lab-dex.yaml. Public by design.
REGISTRATION_TOKEN = "lab-only-registration-token"
DEX_USER = "admin@example.com"
DEX_PASSWORD = "password"
# Loopback redirect target for the smoke's OAuth client. Never actually
# served: the flow stops at the redirect and parses the code from Location.
CALLBACK = "http://127.0.0.1:18763/callback"
FORWARD_PORT = 18443

DEPLOY_TIMEOUT = 900
HEALTH_TIMEOUT = 600


# ---------------------------------------------------------------------------
# Cluster preparation and chart install
# ---------------------------------------------------------------------------


@pytest.fixture(scope="module", autouse=True)
def log_heartbeat() -> Any:
    """One log line a minute, so CircleCI's no-output timeout (10m) never
    fires during the long, otherwise-silent install waits (pytest live
    logging forwards records from any thread)."""
    stop = threading.Event()

    def beat() -> None:
        minutes = 0
        while not stop.wait(60):
            minutes += 1
            logger.info("heartbeat: %d min elapsed, still waiting/working", minutes)

    thread = threading.Thread(target=beat, name="log-heartbeat", daemon=True)
    thread.start()
    yield
    stop.set()


@pytest.fixture(scope="module")
def prerequisites(kube_cluster: Cluster) -> None:
    """Apply the quick start's prerequisites; idempotent.

    Order matters and mirrors the README: Gateway API CRDs, then the lab Dex
    manifest (which waits on the cert-gen Job), then a CoreDNS restart so the
    *.127.0.0.1.nip.io rewrite is live before anything resolves it.
    """
    kube_cluster.kubectl("apply", filename=GATEWAY_API_CRDS, output_format="")
    kube_cluster.kubectl(
        "apply",
        filename=str(REPO_ROOT / "prerequisites" / "lab-dex.yaml"),
        output_format="",
    )
    kube_cluster.kubectl(
        f"-n {NAMESPACE} wait --for=condition=complete --timeout=300s "
        "job/lab-dex-cert-gen",
        output_format="",
    )
    kube_cluster.kubectl(
        "-n kube-system rollout restart deployment coredns", output_format=""
    )
    kube_cluster.kubectl(
        "-n kube-system rollout status deployment coredns --timeout=120s",
        output_format="",
    )


@pytest.fixture(scope="module")
def app_deployment(
    kube_cluster: Cluster,
    app_factory: AppFactoryFunc,
    chart_version: str,
    prerequisites: None,
) -> ConfiguredApp:
    """Install the candidate chart with examples/kind-lab-dex.yaml.

    The chart archive was uploaded to the in-cluster chartmuseum by ATS; a
    separately named Catalog CR avoids clashing with the one apptestctl owns.
    """
    values = yaml.safe_load(
        (REPO_ROOT / "examples" / "kind-lab-dex.yaml").read_text()
    )
    return app_factory(
        APP_NAME,
        chart_version,
        "chartmuseum-smoke",
        NAMESPACE,
        CHARTMUSEUM_URL,
        timeout_sec=900,
        namespace=NAMESPACE,
        deployment_namespace=NAMESPACE,
        config_values=values,
    )


def _deployments(kube_client: pykube.HTTPClient, namespace: str) -> List[Any]:
    return list(pykube.Deployment.objects(kube_client).filter(namespace=namespace))


def _deployment_ready(dep: Any) -> bool:
    status = dep.obj.get("status", {})
    spec_replicas = dep.obj.get("spec", {}).get("replicas", 1)
    return (
        status.get("observedGeneration", 0) >= dep.obj["metadata"]["generation"]
        and status.get("readyReplicas", 0) >= spec_replicas
    )


def _dump_debug(kube_cluster: Cluster, not_ready: Set[str]) -> None:
    """Best-effort state dump when the readiness wait times out, so the CI
    log explains itself (ATS's own diagnostics run after teardown started)."""
    commands = [
        f"-n {NAMESPACE} get pods -o wide",
        f"-n {KAGENT_NAMESPACE} get pods -o wide",
        f"-n {NAMESPACE} get events --sort-by=.lastTimestamp",
    ]
    commands += [
        f"-n {n.split('/', 1)[0]} describe deployment {n.split('/', 1)[1]}"
        for n in sorted(not_ready)
    ]
    for cmd in commands:
        try:
            logger.error("$ kubectl %s\n%s", cmd, kube_cluster.kubectl(cmd, output_format=""))
        except Exception as exc:  # diagnostics must never mask the assertion
            logger.error("kubectl %s failed: %s", cmd, exc)


def wait_for_all_deployments_ready(
    kube_cluster: Cluster, timeout: int = DEPLOY_TIMEOUT
) -> Set[str]:
    """Every Deployment in the platform namespaces is Ready; returns their names.

    Discovers the set dynamically (subchart names vary with the release name)
    but insists on the fixed-name core so an empty namespace cannot pass.
    """
    required = {
        f"{NAMESPACE}/muster",
        f"{NAMESPACE}/muster-valkey",
        # The data-plane Deployment the agentgateway control plane provisions
        # for the chart-owned edge Gateway (same name as the Gateway).
        f"{NAMESPACE}/agentgateway",
        f"{NAMESPACE}/lab-dex",
    }
    kube_client = kube_cluster.kube_client
    deadline = time.monotonic() + timeout
    names: Set[str] = set()
    not_ready: Set[str] = set()
    polls = 0
    while time.monotonic() < deadline:
        deps = _deployments(kube_client, NAMESPACE) + _deployments(
            kube_client, KAGENT_NAMESPACE
        )
        names = {f"{d.namespace}/{d.name}" for d in deps}
        not_ready = {
            f"{d.namespace}/{d.name}" for d in deps if not _deployment_ready(d)
        }
        polls += 1
        if polls % 6 == 0:
            logger.info(
                "waiting for Deployments: missing=%s notReady=%s",
                sorted(required - names),
                sorted(not_ready),
            )
        if required <= names and not not_ready:
            # kagent (its own namespace) and Backstage names derive from the
            # release; assert their presence loosely.
            assert any(n.startswith(f"{KAGENT_NAMESPACE}/") for n in names), (
                f"no kagent Deployments found in {sorted(names)}"
            )
            assert any("backstage" in n for n in names), (
                f"no Backstage Deployment found in {sorted(names)}"
            )
            return names
        time.sleep(10)
    _dump_debug(kube_cluster, not_ready or (required - names))
    raise AssertionError(
        f"Deployments not Ready after {timeout}s: "
        f"missing={sorted(required - names)} notReady={sorted(not_ready)} "
        f"seen={sorted(names)}"
    )


# ---------------------------------------------------------------------------
# Reaching the edge Gateway
# ---------------------------------------------------------------------------


class Edge:
    """HTTP access to the chart's edge Gateway through a kubectl port-forward."""

    def __init__(self, kube_config_path: str, ca_path: str, port: int) -> None:
        self.kube_config_path = kube_config_path
        self.ca_path = ca_path
        self.port = port
        self._proc: Optional[subprocess.Popen] = None

    def url(self, host: str, path: str = "") -> str:
        return self.pin(f"https://{host}.{DOMAIN}{path}")

    def pin(self, url: str) -> str:
        """Force the forward's port onto a lab URL (they are minted portless)."""
        parts = urlsplit(url)
        if parts.scheme != "https" or not (parts.hostname or "").endswith(DOMAIN):
            return url
        if parts.port is not None or self.port == 443:
            return url
        netloc = f"{parts.hostname}:{self.port}"
        return urlunsplit(
            (parts.scheme, netloc, parts.path, parts.query, parts.fragment)
        )

    def start(self) -> None:
        self.stop()
        self._proc = subprocess.Popen(  # nosec
            [
                "kubectl",
                f"--kubeconfig={self.kube_config_path}",
                "-n",
                NAMESPACE,
                "port-forward",
                "service/agentgateway",
                f"{self.port}:443",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
        )
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if self._proc.poll() is not None:
                stderr = (self._proc.stderr.read() if self._proc.stderr else "")[:500]
                raise AssertionError(
                    f"kubectl port-forward exited with {self._proc.returncode}: {stderr}"
                )
            try:
                with socket.create_connection(("127.0.0.1", self.port), timeout=2):
                    return
            except OSError:
                time.sleep(1)
        raise AssertionError("port-forward did not start listening")

    def stop(self) -> None:
        if self._proc is not None:
            self._proc.terminate()
            self._proc.wait(timeout=10)
            self._proc = None

    def request(self, method: str, url: str, **kwargs: Any) -> requests.Response:
        """One request with the lab CA; respawns the forward once if it died."""
        kwargs.setdefault("verify", self.ca_path)
        kwargs.setdefault("timeout", 30)
        try:
            return requests.request(method, self.pin(url), **kwargs)
        except requests.exceptions.ConnectionError:
            logger.warning("connection to the port-forward failed; respawning it")
            self.start()
            return requests.request(method, self.pin(url), **kwargs)


@pytest.fixture(scope="module")
def edge(kube_cluster: Cluster, tmp_path_factory: pytest.TempPathFactory) -> Any:
    """Port-forward to the edge Gateway Service, trusting the lab CA.

    Depends only on cluster state (Service + CA Secret exist), so it serves
    both the smoke (chart installed by app_deployment) and the post-upgrade
    stage (chart installed by ATS).
    """
    deadline = time.monotonic() + 600
    while time.monotonic() < deadline:
        # Ready endpoints, not just the Service: a port-forward against a
        # Service without ready pods exits immediately.
        ep = pykube.Endpoint.objects(
            kube_cluster.kube_client, namespace=NAMESPACE
        ).get_or_none(name="agentgateway")
        if ep is not None and any(
            s.get("addresses") for s in ep.obj.get("subsets") or []
        ):
            break
        time.sleep(5)
    else:
        raise AssertionError(
            "the edge Gateway Service 'agentgateway' never got ready endpoints"
        )

    ca_secret = pykube.Secret.objects(
        kube_cluster.kube_client, namespace=NAMESPACE
    ).get_by_name("agent-platform-idp-ca")
    ca_path = tmp_path_factory.mktemp("lab-ca") / "ca.crt"
    ca_path.write_bytes(base64.b64decode(ca_secret.obj["data"]["ca.crt"]))

    e = Edge(kube_cluster.kube_config_path, str(ca_path), FORWARD_PORT)
    e.start()
    yield e
    e.stop()


def wait_for_muster_healthy(edge: Edge, timeout: int = HEALTH_TIMEOUT) -> None:
    """muster's OAuth server answers 503 until OIDC discovery against the lab
    Dex succeeds (and the whole chain — CoreDNS rewrite, edge listener, Dex —
    has to be up for that), so poll /health until it reports ok."""
    deadline = time.monotonic() + timeout
    last = "no response yet"
    while time.monotonic() < deadline:
        try:
            r = edge.request("GET", edge.url("muster", "/health"))
            last = f"{r.status_code} {r.text[:200]}"
            if r.ok and r.json().get("status") == "ok":
                return
        except requests.exceptions.RequestException as exc:  # keep polling
            last = str(exc)
        time.sleep(10)
    raise AssertionError(f"muster never became healthy; last answer: {last}")


def _dump_auth_logs(kube_cluster: Cluster) -> None:
    """muster and lab-dex logs, for diagnosing a failed login flow."""
    for target in ("deployment/muster", "deployment/lab-dex"):
        try:
            out = kube_cluster.kubectl(
                f"-n {NAMESPACE} logs {target} --tail=120", output_format=""
            )
            logger.error("logs of %s (tail):\n%s", target, out)
        except Exception as exc:  # diagnostics must never mask the assertion
            logger.error("fetching logs of %s failed: %s", target, exc)


def heal_backstage_startup_race(kube_cluster: Cluster) -> None:
    """Restart Backstage if it lost the boot race against the edge Gateway.

    Backstage's GS auth module probes the Dex issuer at startup with a bounded
    retry budget (5 attempts, ~15s) and is designed to crash the pod when the
    issuer stays unreachable — but the failed backend keeps serving liveness
    200 / readiness 503 without exiting, so the pod never restarts (upstream
    giantswarm/backstage issue). On a fresh install Backstage can boot before
    the chart's edge Gateway (which fronts the lab Dex) is up. Callers invoke
    this once the edge is provably healthy; the restart is skipped when
    Backstage made it on its own.
    """
    backstage = [
        d
        for d in _deployments(kube_cluster.kube_client, NAMESPACE)
        if "backstage" in d.name
    ]
    for dep in backstage:
        if _deployment_ready(dep):
            continue
        logger.info("restarting %s: it likely lost the startup race", dep.name)
        kube_cluster.kubectl(
            f"-n {NAMESPACE} rollout restart deployment {dep.name}",
            output_format="",
        )


# ---------------------------------------------------------------------------
# The auth assertions (shared by smoke and post-upgrade)
# ---------------------------------------------------------------------------


def assert_unauthenticated_mcp_401(edge: Edge) -> None:
    """No token -> 401 with the RFC 9728 WWW-Authenticate discovery chain."""
    r = edge.request("POST", edge.url("muster", "/mcp"))
    assert r.status_code == 401, f"expected 401, got {r.status_code}: {r.text[:300]}"
    challenge = r.headers.get("WWW-Authenticate", "")
    prm_url = f"https://muster.{DOMAIN}/.well-known/oauth-protected-resource"
    assert f'resource_metadata="{prm_url}"' in challenge, challenge
    assert 'error="invalid_token"' in challenge, challenge

    # Follow the chain: protected-resource metadata -> authorization server
    # metadata -> the endpoints the login flow uses.
    prm = edge.request("GET", prm_url)
    assert prm.status_code == 200, prm.text[:300]
    servers = prm.json()["authorization_servers"]
    assert servers == [f"https://muster.{DOMAIN}"], servers

    asm = edge.request(
        "GET", f"{servers[0]}/.well-known/oauth-authorization-server"
    )
    assert asm.status_code == 200, asm.text[:300]
    meta = asm.json()
    for key in ("authorization_endpoint", "token_endpoint", "registration_endpoint"):
        assert key in meta, f"{key} missing from AS metadata: {meta}"


def login_and_get_token(edge: Edge) -> str:
    """The full muster OAuth flow with a lab Dex static user, headless.

    Dynamic client registration (with the lab registration token), the
    authorization code + PKCE dance, and the Dex password form — the same
    path a browser login takes, minus the browser.
    """
    # RFC 7591 dynamic client registration. The registration token authorizes
    # confidential clients only (public clients additionally need
    # allowPublicClientRegistration or a trusted allowlist), so register with
    # a client secret.
    r = edge.request(
        "POST",
        edge.url("muster", "/oauth/register"),
        headers={"Authorization": f"Bearer {REGISTRATION_TOKEN}"},
        json={
            "client_name": "ats-smoke",
            "redirect_uris": [CALLBACK],
            "token_endpoint_auth_method": "client_secret_basic",
            "grant_types": ["authorization_code"],
            "response_types": ["code"],
        },
    )
    assert r.status_code in (200, 201), f"DCR failed: {r.status_code} {r.text[:300]}"
    client_id = r.json()["client_id"]
    client_secret = r.json()["client_secret"]

    verifier = base64.urlsafe_b64encode(secrets.token_bytes(32)).rstrip(b"=").decode()
    challenge = (
        base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest())
        .rstrip(b"=")
        .decode()
    )
    # muster rejects authorization requests whose state is shorter than 24
    # characters; token_urlsafe(24) yields 32.
    state = secrets.token_urlsafe(24)

    session = requests.Session()
    session.verify = edge.ca_path
    url = edge.url("muster", "/oauth/authorize") + "?" + urlencode(
        {
            "response_type": "code",
            "client_id": client_id,
            "redirect_uri": CALLBACK,
            "state": state,
            "code_challenge": challenge,
            "code_challenge_method": "S256",
            "scope": "openid profile email groups",
        }
    )
    # Walk the redirects by hand: muster -> Dex -> login form -> Dex approval
    # (skipped) -> muster callback -> our loopback redirect, which is never
    # fetched — the code is parsed straight from the Location header.
    for _ in range(15):
        if url.startswith(CALLBACK):
            break
        r = session.get(edge.pin(url), allow_redirects=False, timeout=30)
        if r.status_code in (301, 302, 303, 307, 308):
            url = urljoin(url, r.headers["Location"])
            continue
        assert r.status_code == 200, f"{url} -> {r.status_code}: {r.text[:300]}"
        # The Dex login form. Submitting it with the static user's credentials
        # is the whole login.
        action = re.search(r'action="([^"]+)"', r.text)
        assert action, f"no form to submit on {url}: {r.text[:300]}"
        post_url = urljoin(url, html.unescape(action.group(1)))
        r = session.post(
            edge.pin(post_url),
            data={"login": DEX_USER, "password": DEX_PASSWORD},
            allow_redirects=False,
            timeout=30,
        )
        assert r.status_code in (302, 303), (
            f"Dex login failed: {r.status_code} {r.text[:300]}"
        )
        url = urljoin(post_url, r.headers["Location"])
    else:
        raise AssertionError(f"OAuth flow never reached the redirect URI: {url}")

    query = parse_qs(urlsplit(url).query)
    assert query.get("state") == [state], f"state mismatch in {url}"
    # An error outcome redirects here too, with error/error_description
    # instead of a code — surface the whole redirect for diagnosis.
    assert "code" in query, f"authorization did not yield a code: {url}"
    code = query["code"][0]

    r = edge.request(
        "POST",
        edge.url("muster", "/oauth/token"),
        auth=(client_id, client_secret),
        data={
            "grant_type": "authorization_code",
            "code": code,
            "redirect_uri": CALLBACK,
            "client_id": client_id,
            "code_verifier": verifier,
        },
    )
    assert r.status_code == 200, f"token exchange failed: {r.status_code} {r.text[:300]}"
    return r.json()["access_token"]


def assert_login_reaches_mcp(edge: Edge) -> None:
    token = login_and_get_token(edge)
    r = edge.request(
        "POST",
        edge.url("muster", "/mcp"),
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        },
        data=json.dumps(
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2025-06-18",
                    "capabilities": {},
                    "clientInfo": {"name": "ats-smoke", "version": "1"},
                },
            }
        ),
    )
    assert r.status_code == 200, f"/mcp with a token: {r.status_code} {r.text[:300]}"
    assert r.headers.get("Mcp-Session-Id"), (
        f"no MCP session id — muster rejected the token: {r.text[:300]}"
    )


# ---------------------------------------------------------------------------
# Smoke
# ---------------------------------------------------------------------------


@pytest.mark.smoke
def test_api_working(kube_cluster: Cluster) -> None:
    assert kube_cluster.kube_client is not None
    assert len(pykube.Node.objects(kube_cluster.kube_client)) >= 1


@pytest.mark.smoke
def test_deployments_ready(
    kube_cluster: Cluster, app_deployment: ConfiguredApp, edge: Edge
) -> None:
    # Prove the auth chain (edge listener, CoreDNS rewrite, lab Dex, muster's
    # OIDC discovery) before judging Backstage: its startup depends on it.
    wait_for_muster_healthy(edge)
    heal_backstage_startup_race(kube_cluster)
    names = wait_for_all_deployments_ready(kube_cluster)
    logger.info("Ready deployments: %s", sorted(names))


@pytest.mark.smoke
@pytest.mark.flaky(reruns=2, reruns_delay=30)
def test_unauthenticated_mcp_gets_401_with_discovery_chain(
    app_deployment: ConfiguredApp, edge: Edge
) -> None:
    wait_for_muster_healthy(edge)
    assert_unauthenticated_mcp_401(edge)


@pytest.mark.smoke
@pytest.mark.flaky(reruns=2, reruns_delay=30)
def test_static_user_login_reaches_mcp(
    kube_cluster: Cluster, app_deployment: ConfiguredApp, edge: Edge
) -> None:
    wait_for_muster_healthy(edge)
    try:
        assert_login_reaches_mcp(edge)
    except BaseException:
        _dump_auth_logs(kube_cluster)
        raise


@pytest.mark.smoke
def test_kagent_agent_reaches_ready(
    kube_cluster: Cluster, app_deployment: ConfiguredApp
) -> None:
    """A minimal declarative Agent against the default ModelConfig.

    The provider is fake (a made-up Anthropic key in the Secret the chart's
    default ModelConfig references); Ready means the controller accepted and
    reconciled the Agent, not that a model call succeeded.
    """
    fake_provider_secret = yaml.safe_dump(
        {
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {
                "name": "kagent-anthropic",
                "namespace": KAGENT_NAMESPACE,
            },
            "stringData": {"ANTHROPIC_API_KEY": "lab-only-fake-anthropic-key"},
        }
    )
    kube_cluster.kubectl(
        "apply", std_input=fake_provider_secret, filename="-", output_format=""
    )
    agent = yaml.safe_dump(
        {
            "apiVersion": "kagent.dev/v1alpha2",
            "kind": "Agent",
            "metadata": {"name": "ats-smoke-agent", "namespace": KAGENT_NAMESPACE},
            "spec": {
                "description": "ATS smoke agent (lab only)",
                "type": "Declarative",
                "declarative": {
                    "modelConfig": "default-model-config",
                    "systemMessage": "You are the ATS smoke agent.",
                },
            },
        }
    )
    kube_cluster.kubectl("apply", std_input=agent, filename="-", output_format="")
    kube_cluster.kubectl(
        f"-n {KAGENT_NAMESPACE} wait --for=condition=Ready --timeout=600s "
        "agents.kagent.dev/ats-smoke-agent",
        output_format="",
    )


# ---------------------------------------------------------------------------
# Upgrade: last published chart -> candidate (ATS drives the App CR; the CRD
# re-apply one-liner runs in tests/ats/upgrade-hook.sh between the stages)
# ---------------------------------------------------------------------------


@pytest.mark.upgrade
def test_upgrade(
    kube_cluster: Cluster,
    test_extra_info: Dict[str, str],
    request: pytest.FixtureRequest,
) -> None:
    stage = test_extra_info.get("upgrade_test_stage", "")
    if stage == "pre_upgrade":
        # The stable release is installed; put the prerequisites in place so
        # the upcoming candidate upgrade lands on a prepared cluster (the
        # cluster is fresh when the upgrade scenario runs on its own).
        request.getfixturevalue("prerequisites")
        return

    assert stage == "post_upgrade", f"unexpected upgrade stage {stage!r}"
    request.getfixturevalue("prerequisites")
    edge_fixture: Edge = request.getfixturevalue("edge")
    wait_for_muster_healthy(edge_fixture)
    heal_backstage_startup_race(kube_cluster)
    wait_for_all_deployments_ready(kube_cluster)
    assert_unauthenticated_mcp_401(edge_fixture)
    try:
        assert_login_reaches_mcp(edge_fixture)
    except BaseException:
        _dump_auth_logs(kube_cluster)
        raise
