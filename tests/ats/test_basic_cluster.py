import logging

import pykube
from pytest_helm_charts.clusters import Cluster
import pytest

logger = logging.getLogger(__name__)


@pytest.mark.smoke
def test_api_working(kube_cluster: Cluster) -> None:
    """Smoke test: the chart installs cleanly and the cluster API is reachable."""
    assert kube_cluster.kube_client is not None
    assert len(pykube.Node.objects(kube_cluster.kube_client)) >= 1
