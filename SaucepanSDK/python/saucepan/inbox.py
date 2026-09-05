class InboxResource:
    def __init__(self, http):
        self._http = http

    def poll(self):
        """
        Return all unacknowledged deliveries — the normal "what needs my attention" call.

        A delivery stays in poll() results until delivery.acknowledge() is called.
        Safe to call repeatedly; already-acknowledged items will not reappear.

        Prefer ``drain()`` so completed FITS are downloaded before ack — hot URLs
        may expire or be deleted later; this reference SDK has no recovery path.

        Returns:
            list[Delivery]  — may be empty if nothing new has arrived

        Example:
            for delivery in client.inbox.drain("/data/fits/"):
                print(delivery.task_id, delivery.local_path)
        """
        return self._http.poll_inbox()

    def poll_all(self):
        """
        Return every delivery ever received, including acknowledged ones.
        Useful for auditing, replaying, or debugging.

        Returns:
            list[Delivery]
        """
        return self._http.poll_inbox_all()

    def drain(self, directory: str, *, acknowledge: bool = True):
        """
        Poll unacked deliveries, **download completed FITS by default**, then ack.

        Hot storage may be deleted after a retention window — save locally first.
    There is no fallback when the hot object is already gone.

        Returns:
            list[Delivery] processed in this pass (including failed status items)
        """
        deliveries = self.poll()
        for delivery in deliveries:
            if delivery.status == "completed":
                path = delivery.download(directory)
                delivery.local_path = path
            if acknowledge:
                delivery.acknowledge()
        return deliveries
