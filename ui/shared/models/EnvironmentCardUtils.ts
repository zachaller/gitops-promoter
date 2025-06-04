// //TODO:Currently calls GitHub API 
export async function fetchCommitDetails(owner: string, repo: string, sha: string): Promise<{ author: string, message: string } | null> {
  try {
    const res = await fetch(`https://api.github.com/repos/${owner}/${repo}/commits/${sha}`);
    if (!res.ok) return null;
    const commit = await res.json();
    return {
      author: commit.commit.author.name,
      message: commit.commit.message,
    };
  } catch {
    return null;
  }
}

// Merge checks from active and proposed
export function mergeChecks(active: any[] = [], proposed: any[] = []): any[] {
  const checksMap = new Map();
  for (const check of proposed) {
    checksMap.set(check.name, check);
  }
  for (const check of active) {
    if (!checksMap.has(check.name)) {
      checksMap.set(check.name, check);
    }
  }
  return Array.from(checksMap.values());
}



// // GETS DRY/HYDRATED COMMIT DETAILS
async function getCommitEnrichment(owner: string, repoName: string, sha: string): Promise<{ author: string, message: string, url: string }> {
  let author = '-';
  let message = '-';
  let url = '';
  if (owner && repoName && sha) {
    url = `https://github.com/${owner}/${repoName}/commit/${sha}`;
    const commit = await fetchCommitDetails(owner, repoName, sha);
    if (commit) {
      author = commit.author;
      message = commit.message;
    }
  }
  return { author, message, url };
}

async function getURL(owner: string, repoName: string, sha: string): Promise<string> {
  return `https://github.com/${owner}/${repoName}/commit/${sha}`;
}


// Fill data for a single environment
export async function getEnvironmentDetails(ctp: any, gitRepositories: any[], namespace: string): Promise<any> {
  const drySha = ctp.status?.active?.dry?.sha;
  const hydratedSha = ctp.status?.active?.hydrated?.sha;

  const gitRepo = gitRepositories.find(
    (repo: any) => repo.metadata.name === ctp.spec.gitRepositoryRef.name
  );
  const owner = gitRepo?.spec?.github?.owner || '';
  const repoName = gitRepo?.spec?.github?.name || '';

  const { author: dryCommitAuthor, message: dryCommitMessage, url: dryCommitUrl } = await getCommitEnrichment(owner, repoName, drySha);
  const { author: hydratedCommitAuthor, message: hydratedCommitMessage, url: hydratedCommitUrl } = await getCommitEnrichment(owner, repoName, hydratedSha);


  //Comment Code
  // const dryCommitAuthor = '-';
  // const dryCommitMessage = '-';
  // const dryCommitUrl = await getURL(owner, repoName, drySha);
  // const hydratedCommitAuthor = '-';
  // const hydratedCommitMessage = '-';
  // const hydratedCommitUrl = await getURL(owner, repoName, hydratedSha);
  

  // Check if any proposed commit status is pending
  const proposedCommitStatusesRaw = ctp.status?.proposed?.commitStatuses || [];
  const hasProposedPending = proposedCommitStatusesRaw.some((cs: any) => cs.phase === 'pending');

  // Build active commit statuses, but override to 'pending' if needed
  let activeCommitStatuses = (ctp.status?.active?.commitStatuses || []).map((cs: any) => ({
    name: cs.key,
    status: cs.phase || 'unknown',
  }));

  if (hasProposedPending) {
    activeCommitStatuses = activeCommitStatuses.map((cs: any) => ({
      ...cs,
      status: 'pending',
    }));
  }

  // Build proposed commit statuses as usual
  const proposedCommitStatuses = proposedCommitStatusesRaw.map((cs: any) => ({
    name: cs.key,
    status: cs.phase || 'unknown',
  }));

  const checks = mergeChecks(activeCommitStatuses, proposedCommitStatuses);
  const checksInProgress = checks.filter((c: any) => c.status === 'pending').length;
  const totalChecks = checks.length;



  return {
    ...ctp,
    branch: ctp.spec.activeBranch,
    autoMerge: ctp.spec.autoMerge,
    dryCommitAuthor,
    dryCommitMessage,
    dryCommitUrl,
    hydratedCommitAuthor,
    hydratedCommitMessage,
    hydratedCommitUrl,
    checks,
    checksInProgress,
    totalChecks,
    active: ctp.status?.active,
    proposed: ctp.status?.proposed,
  };
}




//Clear indicator -> whether it's been promoted and what the state of the environment 
// Apps -> which apps are in the environment (Intuit will be 1-1)
// Phase 1 -> URL -> consturcted and on teh stauses on the resources
// Knowing IF PR is merged or PR is open
// dry ACTIVE sha on the enviornment card is what the users understand
// No hydrated sha on the environment sha
// indicate clearly
// Have PR shown in the dashboard
// In the bottom between 2 stasues and put the concept of a PR


// Concept of a PR -> Box on the left (dev PR). in BETWEEN dev and staging -> Staging PR (with 2 sHA). As those PRs disappear, we can show the change being promoted throughout the environment (The dry sha) starting to match up. Checks becoming uflly healthy. 
// Look to see if it's been promoted.
// we just need to display the ID clearly
//When a PR is open -> 

//commit sha is the most important thing and doesn't 

// how to dispaly the 10 commit -> List of 10 things to simplified

//Active checks -> In evnrionemnt card 
//Proposed checks -> in the PR

//Proposed Checks will be brought up over -> Each envionrment card will have a heart health that's the aggregate checks for each environment
//release view 
// deploymnet view

//Must be very clear on how we displayed on what's running in each environment
//sTATE of promotion


//Is promoted = True (Changing colors) or if the pR disappears (meaning it's been merged)

// Exist for things like history -> Checks commit that live in PR 

//