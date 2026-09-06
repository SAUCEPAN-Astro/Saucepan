def run():
    mean = read_frame("mean")
    peak = read_frame("max")
    if mean > 5000 and peak > 60000:
        board_post("saturated frame")
        next_capture(exposure_sec=10)
    elif mean < 1000:
        board_post("dark frame")
