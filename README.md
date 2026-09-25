# SAUCEPAN
Small-Aperture distribUted Compute-Enabled Public Astronomy Network. 

Saucepan is designed to let researchers access a toolkit to access a network of free **volunteer** small aperture telescopes. It is inspired by projects like Folding@Home and BOINC and focuses purely on providing a very open interface, allowing researchers to tailor the specifics to their work.   <br>
Automated task assignment at high speeds and matching. configurable scientific and telescope constraints for tasks allowing selection by aperture, filters, FWHM/seeing, resolution, field of view, exposure limits, altitude/horizon safety, quality tier, site geography, cohort similarity, and scarcity-aware selection.<br>
Multiple science products: per-frame results, time-binned products, or stacked images. <br>
Pier operator tooling: a lightweight CLI for status, constraints, project participation, and other features<br> 
Multiple scopes per task allowing for higher SNR (with stacking), multiple geographic locations or 24/7 coverage<br> 
Normalization pipeline to process heterogeneous data <br>
Edge code and inter-telescope communication via message board <br>
Hardware integration: a resident pier agent with ASCOM Alpaca support, telemetry, FITS writing, signed commands, and safety-gated mount operations.<br>
Ability to maintain private distributed telescope fleet by research groups* <br>

*not a feature that is fully fleshed out in this reference implementation<br>



<img width="11507" height="9682" alt="Untitled-2026-09-25-1628" src="https://github.com/user-attachments/assets/743ea5ed-17ab-4c43-ad5e-db27ccc5ee56" />



# Parked project
I am a highschool student, and do not have the skillset, time or resources to fully realize this project, and hence this project remains parked. If anyone is interested in forking the project/ working on/with this project please do reach out to me, I would love to be in the loop as to where this project goes. https://discord.gg/Z4cJxczXBq 

If resources and time do permit me, I will love to continue working on this project. The current blockers are: <br> 
1. A team member that knows more about programming than I do, and is interested in working on this project <br>
2. Access to a GOTO telescope mount to test the client side features
3. A tangible use case, where this network may actually help researchers; some preliminary research has lead me to some however I hope to be able to reach out to some individuals that are in the field and get thier opinion on its use case first. 


# Introduction 

Any authorized user is able to programmatically create tasks, which will be sent to the central task server, a matching algorithm will be used to select the best volunteer scope for the task based on the details set by the user. FITS data be uploaded to a storage service, normalized, and researcher machine can poll for these downloads automatically. In the event the volunteer telescope drops a substitute telescope is brought in its place with high priority however uninterrupted coverage cannot be guaranteed until a large number of scopes are available. Multiple telescopes may be used for a task either to improve SNR or (more importantly) providing multiple perspectives, and also continuous coverage to monitor points of interest for longer duration of time. Saucepan aims to calibrate and normalize data in a way that ensures that the hetrogenous nature of the network does not lead to unreliable data products. A production implementation may look at further optimizing this process to improve quality. <br>

**End-to-end flow**: researcher SDK → campaign/task server → matched pier → capture → short-lived R2 buffer → grading/normalization → researcher SDK inbox. R2 is not a long-term archive
<br>

# REFERENCE IMPLEMENTATION 
This code base is purely a reference implementation of an idea that I have had. This is **NOT** safe or ready for deployment. The code base has many security flaws, some inherit to how it operates at the moment (i.e allowing anyone registered as a 'researcher' can run code on volunteer machines* as of the current implementation does not have a production hardened sandox or resource limitation. Furthermore there is no current systems in place to properly authorize 'researchers'. The future implementation holds a manual one time authorization via email for any researcher.) Many similar flaws may be present. Additionally large chunks of the code base have been built with the assistance of AI and hence must not be fully trusted until a proper audit. 
The current implementation gives all authorized users access to the highest task priority and entrusts them to appropriately assign priorities for their tasks; a system that will have to be changed during a proper implementation. <br>
This code has not been tested on a live fleet, or ever run with actual telescopes rather only with simulated nodes to ensure wiring and logic hold.<br>


*The purpose of this feature is to allow lower latency edge compute, which may be useful for time-sensitive discoveries; coupled with the ability of volunteer machines (piers) to communicate with each other over a 
message board this allows for anomaly/Object of interest detection programs to be written specifically for each campaign <br>

# Example SDK usage 

```from saucepan import TaskSpec

task = client.add_task(
    campaign_id="<CAMPAIGN_ID>",
    spec=TaskSpec(
        name="<TASK_LABEL>",  #arbitrary string; only useful for organization
        normalized_integration_budget_s=<TOTAL_2M_EQUIVALENT_SECONDS>, 
        filters=["<FILTER>"],
        min_power=0.0,
    ),
    target_ra=<RA_DEGREES>,
    target_dec=<DEC_DEGREES>,
)
```

# Contact 
**Discord:** https://discord.gg/Z4cJxczXBq <br>
**LinkedIN:** https://www.linkedin.com/in/vyom-bagrecha-899704383/ <br>
**Email**: [Email Me (alias)](mailto:saucepan.headscarf744@slmail.me)


