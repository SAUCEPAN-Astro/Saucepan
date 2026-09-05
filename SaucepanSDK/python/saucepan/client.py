import saucepan._http as _http
import saucepan.inbox as inbox
import saucepan.models as models
import saucepan.tasks as tasks

DEFAULT_BASE_URL = "http://127.0.0.1:8080/api/v1"


class Client:
    """
    Entry point for the Saucepan developer SDK.

    Usage:
        import saucepan
        client = saucepan.Client("sp_live_...")

        # Submit an observation
        task = client.tasks.submit(
            name="M42",
            integration_time=300,
            min_power=0.7,
        )

        # Later — drain inbox (downloads completed FITS before ack by default)
        for delivery in client.inbox.drain("/data/fits/"):
            print(delivery.task_id, delivery.local_path)
    """

    def __init__(
        self,
        api_key: str,
        base_url: str = DEFAULT_BASE_URL,
        timeout: float = 30.0,
        pool_connections: int = 10,
        pool_maxsize: int = 10,
        max_retries: int = 3,
        retry_backoff_base: float = 1.5,
    ) -> None:
        """
        Initialize Saucepan client.

        Args:
            api_key: Your Saucepan API key (format: sp_live_...)
            base_url: API base URL. Override for another task-server instance.
            timeout: HTTP request timeout in seconds (default: 30)
            pool_connections: Number of connection pools to cache (default: 10)
            pool_maxsize: Maximum connections per pool (default: 10)
            max_retries: Maximum retry attempts on server errors (default: 3)
            retry_backoff_base: Exponential backoff base in seconds (default: 1.5)

        Raises:
            ValueError: If API key format is invalid
        """
        if not api_key or not api_key.startswith("sp_live_"):
            raise ValueError("Invalid API key format. Keys must start with 'sp_live_'")

        self._session = _http._HttpSession(
            api_key=api_key,
            base_url=base_url,
            timeout=timeout,
            pool_connections=pool_connections,
            pool_maxsize=pool_maxsize,
            max_retries=max_retries,
            retry_backoff_base=retry_backoff_base,
        )
        self.tasks: tasks.TasksResource = tasks.TasksResource(self._session)
        self.inbox: inbox.InboxResource = inbox.InboxResource(self._session)

    def quota(self) -> models.QuotaStatus:
        """
        Check your current quota and rate limit status.

        Returns:
            QuotaStatus with .total, .used, .remaining, .is_exhausted
        """
        return self._session.get_quota()
