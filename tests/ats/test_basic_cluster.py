import logging

import pykube
from pytest_helm_charts.clusters import Cluster
import pytest

logger = logging.getLogger(__name__)


@pytest.mark.smoke
def test_api_working(kube_cluster: Cluster) -> None:
    """Placeholder smoke test.

    The smoke step is skipped via .ats/main.yaml until the kind smoke lands
    (prerequisites installed by the conftest, lab Dex, examples/kind-lab-dex.yaml).
    This test only asserts the cluster API is reachable so tests/ats exists.
    """
    assert kube_cluster.kube_client is not None
    assert len(pykube.Node.objects(kube_cluster.kube_client)) >= 1
